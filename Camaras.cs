// Camaras: shows all cameras of a Dahua NVR full screen, through Dahua P2P (no open ports needed), nothing else.
// Uses the Dahua libraries that SmartPSS Lite installed:
//   P2PDll.dll  -> opens a local tunnel to the NVR by serial number (P2P)
//   dhnetsdk.dll -> logs in through that tunnel and plays live video into windows
// Esc closes. Double click on a camera makes it big / small again.
// Settings in camaras.ini next to the exe. The NVR password is asked once and saved encrypted (Windows DPAPI).
using System;
using System.Collections.Generic;
using System.Drawing;
using System.IO;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Windows.Forms;

static class Native
{
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode)] public static extern bool SetDllDirectory(string path);

    [DllImport("P2PDll.dll", CallingConvention = CallingConvention.Cdecl)]
    public static extern int P2P_Init(int type, string svrIp, int svrPort, string svrKey, string uuid, out long handle);
    [DllImport("P2PDll.dll", CallingConvention = CallingConvention.Cdecl)]
    public static extern int P2P_Connect(int type, long handle, string sn, int remotePort, ref int localPort,
                                         string user, string pass, string randsalt, string devP2PVersion);
    // These return 0 when OK (server connected / device online / tunnel ready).
    [DllImport("P2PDll.dll", CallingConvention = CallingConvention.Cdecl)] public static extern int P2P_QueryServerStatus(int type, long handle);
    [DllImport("P2PDll.dll", CallingConvention = CallingConvention.Cdecl)] public static extern int P2P_DeviceStatus(int type, long handle, string sn);
    [DllImport("P2PDll.dll", CallingConvention = CallingConvention.Cdecl)] public static extern int P2P_QueryChannelStatus(int type, long handle, int localPort);

    // Dahua's log library used by P2PDll (dsl::DPrintLog singleton).
    [DllImport("libdsl.dll", EntryPoint = "?instance@DPrintLog@dsl@@SAPEAV12@XZ")] public static extern IntPtr DslLogInstance();
    [DllImport("libdsl.dll", EntryPoint = "?SetFile@DPrintLog@dsl@@QEAAHPEBD@Z", CharSet = CharSet.Ansi)] public static extern int DslSetFile(IntPtr self, string path);
    [DllImport("libdsl.dll", EntryPoint = "?SetLevel@DPrintLog@dsl@@QEAAHH@Z")] public static extern int DslSetLevel(IntPtr self, int level);

    public delegate void DisConnectCb(long lLoginID, IntPtr pchDVRIP, int nDVRPort, IntPtr dwUser);
    [DllImport("dhnetsdk.dll")] public static extern bool CLIENT_Init(DisConnectCb cb, IntPtr dwUser);
    [DllImport("dhnetsdk.dll")] public static extern void CLIENT_SetAutoReconnect(DisConnectCb cb, IntPtr dwUser);
    [DllImport("dhnetsdk.dll")] public static extern long CLIENT_LoginWithHighLevelSecurity(IntPtr pIn, IntPtr pOut);
    [DllImport("dhnetsdk.dll")] public static extern long CLIENT_RealPlayEx(long lLoginID, int nChannelID, IntPtr hWnd, int rType);
    [DllImport("dhnetsdk.dll")] public static extern int CLIENT_GetLastError();
    [DllImport("dhnetsdk.dll")] public static extern bool CLIENT_StopRealPlayEx(long lRealHandle);
    // Raw stream callback: called with data while a channel sends video.
    public delegate void RealDataCb(long lRealHandle, uint dwDataType, IntPtr pBuffer, uint dwBufSize, long param, IntPtr dwUser);
    [DllImport("dhnetsdk.dll")] public static extern bool CLIENT_SetRealDataCallBackEx2(long lRealHandle, RealDataCb cb, IntPtr dwUser, uint dwFlag);
    [DllImport("dhnetsdk.dll")] public static extern bool CLIENT_SetConnectTime(int nWaitTime, int nTryTimes);
}

static class Win
{
    [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] public static extern short GetAsyncKeyState(int vKey);
    [DllImport("user32.dll")] public static extern IntPtr WindowFromPoint(Point p);
    [DllImport("user32.dll")] public static extern IntPtr GetAncestor(IntPtr hWnd, uint flags);
}

class CamForm : Form
{
    static string Dir = AppDomain.CurrentDomain.BaseDirectory;
    static string LogFile = Path.Combine(Dir, "camaras.log");
    static string PassFile = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData), "camaras", "nvr.bin");
    static Native.DisConnectCb keepCb = (a, b, c, d) => Log("desconectado del NVR");

    Dictionary<string, string> cfg = new Dictionary<string, string>();

    // One square on screen. Half: 0 = whole picture, 1 = top half, 2 = bottom half
    // (for a channel whose picture is two cameras stacked; it is played twice and each square shows one half).
    class Tile { public Panel Box, Video; public int Ch, Half; public long Play; }
    List<Tile> tiles = new List<Tile>();
    Tile big = null;
    Label status;
    long login = 0;
    static string CacheFile = Path.Combine(Dir, "canales.txt");

    // Discovery: count the bytes each channel sends; channels that send video have a camera.
    static Dictionary<long, long> bytesPerPlay = new Dictionary<long, long>();
    static Native.RealDataCb dataCb = (h, type, buf, size, param, user) => { lock (bytesPerPlay) { long b; bytesPerPlay.TryGetValue(h, out b); bytesPerPlay[h] = b + size; } };

    static void Log(string s)
    {
        try { File.AppendAllText(LogFile, DateTime.Now.ToString("yyyy-MM-dd HH:mm:ss.fff") + "  " + s + "\r\n"); } catch { }
    }

    string C(string k, string def) { string v; return cfg.TryGetValue(k, out v) && v != "" ? v : def; }

    public CamForm()
    {
        foreach (var line in File.Exists(Path.Combine(Dir, "camaras.ini")) ? File.ReadAllLines(Path.Combine(Dir, "camaras.ini")) : new string[0])
        {
            int i = line.IndexOf('=');
            if (i > 0 && !line.StartsWith(";")) cfg[line.Substring(0, i).Trim()] = line.Substring(i + 1).Trim();
        }
        Text = "Camaras";
        BackColor = Color.Black;
        FormBorderStyle = FormBorderStyle.None;
        WindowState = FormWindowState.Maximized;
        KeyPreview = true;
        // Dahua's video library puts its own window over each square and that window takes the clicks and keys.
        // So read the mouse button and Esc directly, every 40 ms, while this app is in front.
        bool wasDown = false, escWas = false;
        var poll = new System.Windows.Forms.Timer { Interval = 40 };
        poll.Tick += (s, e) =>
        {
            // Mouse: count the click when the pointer is over this app (any of its windows, also the video ones).
            bool down = (Win.GetAsyncKeyState(0x01) & 0x8001) != 0;   // left mouse button (0x0001 = pressed since last check, catches short clicks)
            if (down && !wasDown)
            {
                var over = Win.GetAncestor(Win.WindowFromPoint(Cursor.Position), 2);   // 2 = the top window
                if (over == Handle) ClickAt(PointToClient(Cursor.Position));
            }
            wasDown = down;
            // Esc: only when this app is in front.
            if (Win.GetForegroundWindow() != Handle) { escWas = false; return; }
            bool esc = (Win.GetAsyncKeyState(0x1B) & 0x8000) != 0;
            if (esc && !escWas) { if (big != null) { big = null; DoLayout(); } else Close(); }
            escWas = esc;
        };
        poll.Start();

        status = new Label { Text = "Conectando...", ForeColor = Color.Gray, AutoSize = true, Location = new Point(20, 20), Font = new Font("Segoe UI", 16) };
        Controls.Add(status);
        Resize += (s, e) => DoLayout();
        Shown += (s, e) => new Thread(Connect) { IsBackground = true }.Start();
    }

    // Channels whose picture is two cameras stacked (top + bottom). Default: channel 2 ("Device1").
    HashSet<int> SplitChannels()
    {
        var set = new HashSet<int>();
        foreach (var c in C("dividir", "").Split(',')) { int ch; if (int.TryParse(c.Trim(), out ch)) set.Add(ch); }
        return set;
    }

    // Must run on the UI thread. Replaces all squares with one per camera, then starts the video.
    void BuildTiles(List<int> chans)
    {
        foreach (var t in tiles) { if (t.Play != 0) Native.CLIENT_StopRealPlayEx(t.Play); Controls.Remove(t.Box); t.Box.Dispose(); }
        tiles.Clear();
        big = null;
        var split = SplitChannels();
        foreach (var ch in chans)
            foreach (var half in split.Contains(ch) ? new[] { 1, 2 } : new[] { 0 })
            {
                var t = new Tile { Ch = ch, Half = half };
                t.Box = new Panel { BackColor = Color.Black };
                t.Video = new Panel { BackColor = Color.Black };
                t.Box.Controls.Add(t.Video);
                tiles.Add(t);
                Controls.Add(t.Box);
            }
        status.Visible = tiles.Count == 0;
        DoLayout();
        int stream = int.Parse(C("flujo", "3"));
        foreach (var t in tiles)
        {
            t.Play = Native.CLIENT_RealPlayEx(login, t.Ch, t.Video.Handle, stream);
            Log("RealPlay canal " + t.Ch + (t.Half > 0 ? (t.Half == 1 ? " (arriba)" : " (abajo)") : "") + " -> " + t.Play + (t.Play == 0 ? " err 0x" + Native.CLIENT_GetLastError().ToString("X") : ""));
        }
    }

    void DoLayout()
    {
        int n = tiles.Count;
        if (n == 0) return;
        int cols = (int)Math.Ceiling(Math.Sqrt(n)), rows = (n + cols - 1) / cols;
        int w = ClientSize.Width / cols, h = ClientSize.Height / rows;
        for (int i = 0; i < n; i++)
        {
            var t = tiles[i];
            if (big != null && t != big) { t.Box.Visible = false; continue; }
            t.Box.Visible = true;
            t.Box.Bounds = big == t ? ClientRectangle : new Rectangle((i % cols) * w + 1, (i / cols) * h + 1, w - 2, h - 2);
            int bw = t.Box.ClientSize.Width, bh = t.Box.ClientSize.Height;
            // A half square holds a video panel twice as tall, shifted so only its half shows.
            t.Video.Bounds = t.Half == 0 ? new Rectangle(0, 0, bw, bh) : new Rectangle(0, t.Half == 1 ? 0 : -bh, bw, bh * 2);
        }
    }

    // Click on a camera: show it alone, full screen. Click again: back to all cameras.
    void ClickAt(Point p)
    {
        if (big != null) { big = null; DoLayout(); return; }
        foreach (var t in tiles) if (t.Box.Visible && t.Box.Bounds.Contains(p)) { big = t; DoLayout(); return; }
    }

    void Status(string s)
    {
        Log(s);
        BeginInvoke((MethodInvoker)delegate { status.Text = s; });
    }

    // Plays every channel for a few seconds without showing it and keeps the ones that send video.
    List<int> DiscoverCameras(int nChan)
    {
        var plays = new Dictionary<long, int>();
        lock (bytesPerPlay) bytesPerPlay.Clear();
        for (int ch = 0; ch < Math.Min(nChan, 64); ch++)
        {
            long h = Native.CLIENT_RealPlayEx(login, ch, IntPtr.Zero, 3);
            if (h == 0) continue;
            Native.CLIENT_SetRealDataCallBackEx2(h, dataCb, IntPtr.Zero, 1);
            plays[h] = ch;
        }
        Thread.Sleep(int.Parse(C("busqueda_segundos", "8")) * 1000);
        var found = new List<int>();
        foreach (var kv in plays)
        {
            long b;
            lock (bytesPerPlay) bytesPerPlay.TryGetValue(kv.Key, out b);
            Native.CLIENT_StopRealPlayEx(kv.Key);
            if (b > 20000) found.Add(kv.Value);
        }
        found.Sort();
        Log("busqueda: " + plays.Count + " canales probados, con video: " + string.Join(",", found));
        return found;
    }

    void Connect()
    {
        try
        {
            string pass = Encoding.UTF8.GetString(ProtectedData.Unprotect(File.ReadAllBytes(PassFile), null, DataProtectionScope.CurrentUser));
            string user = C("usuario", "admin"), sn = C("sn", "");
            if (sn == "" || C("servidores", "") == "") { Status("Falta camaras.ini con sn y servidores (ver camaras.ini.example)"); return; }
            Native.SetDllDirectory(C("dahua", @"C:\Program Files\SmartPSSLite"));

            // Send the P2P library's own log to p2p.log next to the exe (for debugging).
            try { IntPtr lg = Native.DslLogInstance(); Native.DslSetFile(lg, Path.Combine(Dir, "p2p.log")); Native.DslSetLevel(lg, int.Parse(C("nivel_log", "6"))); } catch (Exception ex) { Log("log P2P: " + ex.Message); }

            // 1. P2P tunnel. Try each server until one works.
            int localPort = 0;
            bool ok = false;
            // "server|key|user": the TWO strings SmartPSS passes to P2P_Init (captured with a shim).
            foreach (var entry in C("servidores", "").Split(','))
            {
                var parts = entry.Split('|');
                string srv = parts[0].Trim(), key = parts.Length > 1 ? parts[1].Trim() : "", cuser = parts.Length > 2 ? parts[2].Trim() : "";
                // The P2P library cannot resolve host names on Windows 7 (SmartPSS passes the IP too).
                string ip = srv;
                try { foreach (var a in System.Net.Dns.GetHostAddresses(srv)) if (a.AddressFamily == System.Net.Sockets.AddressFamily.InterNetwork) { ip = a.ToString(); break; } }
                catch (Exception ex) { Log("DNS " + srv + ": " + ex.Message); continue; }
                long h;
                int r = Native.P2P_Init(0, ip, int.Parse(C("puerto_p2p", "8800")), key, cuser, out h);
                Log("P2P_Init " + srv + " (" + ip + ") -> " + r + " handle " + h);
                if (r != 0 || h == 0) continue;
                WaitFor("NVR online", 10, () => Native.P2P_DeviceStatus(0, h, sn));
                localPort = 0;
                r = Native.P2P_Connect(0, h, sn, 37777, ref localPort, user, pass, "", "");
                Log("P2P_Connect -> " + r + " localPort " + localPort);
                if (r != 0 || localPort <= 0) continue;
                int lp = localPort;
                if (WaitFor("tunel", 15, () => Native.P2P_QueryChannelStatus(0, h, lp))) { ok = true; break; }
            }
            if (!ok) { Status("No pude abrir el tunel P2P (ver camaras.log)"); return; }

            // 2. Login through the tunnel.
            Native.CLIENT_Init(keepCb, IntPtr.Zero);
            Native.CLIENT_SetAutoReconnect(keepCb, IntPtr.Zero);
            Native.CLIENT_SetConnectTime(5000, 1);
            IntPtr pin = Marshal.AllocHGlobal(1024), pout = Marshal.AllocHGlobal(1024);
            for (int i = 0; i < 1024; i++) { Marshal.WriteByte(pin, i, 0); Marshal.WriteByte(pout, i, 0); }
            Marshal.WriteInt32(pin, 0, 224);                       // dwSize
            WriteStr(pin, 4, "127.0.0.1", 64);                      // szIP
            Marshal.WriteInt32(pin, 68, localPort);                 // nPort
            WriteStr(pin, 72, user, 64);                            // szUserName
            WriteStr(pin, 136, pass, 64);                           // szPassword
            Marshal.WriteInt32(pin, 200, 0);                        // emSpecCap = TCP
            // The tunnel needs a few seconds after P2P_Connect; retry while the error is "timeout" (3).
            int nErr = 0, nChan = 0;
            for (int attempt = 1; attempt <= 8; attempt++)
            {
                for (int i = 0; i < 1024; i++) Marshal.WriteByte(pout, i, 0);
                Marshal.WriteInt32(pout, 0, 240);                   // dwSize
                login = Native.CLIENT_LoginWithHighLevelSecurity(pin, pout);
                nErr = Marshal.ReadInt32(pout, 104); nChan = Marshal.ReadInt32(pout, 68);
                Log("Login intento " + attempt + " -> " + login + " nError " + nErr);
                if (login != 0 || nErr == 1 || nErr == 2) break;
                Status("Conectando... (intento " + (attempt + 1) + ")");
                Thread.Sleep(1500);
            }
            for (int i = 0; i < 1024; i++) Marshal.WriteByte(pin, i, 0);
            Marshal.FreeHGlobal(pin); Marshal.FreeHGlobal(pout);
            Log("Login -> " + login + " nError " + nErr + " canales " + nChan + " lastErr 0x" + Native.CLIENT_GetLastError().ToString("X"));
            if (login == 0) { Status(nErr == 1 ? "Clave del NVR incorrecta. Borra " + PassFile + " y abri de nuevo." : "No pude entrar al NVR (error " + nErr + ")"); return; }

            // 3. Show the cameras found last time right away, then look for cameras again
            //    (a camera added to the NVR appears; the list is saved for the next start).
            List<int> cached = new List<int>();
            try { foreach (var c in File.ReadAllText(CacheFile).Split(',')) { int ch; if (int.TryParse(c.Trim(), out ch)) cached.Add(ch); } } catch { }
            if (cached.Count > 0) Invoke((MethodInvoker)delegate { BuildTiles(cached); });
            else Status("Buscando camaras...");
            var found = DiscoverCameras(nChan);
            if (found.Count == 0) { if (cached.Count == 0) Status("No encontre camaras con video (ver camaras.log)"); return; }
            if (string.Join(",", found) != string.Join(",", cached))
            {
                try { File.WriteAllText(CacheFile, string.Join(",", found)); } catch { }
                Invoke((MethodInvoker)delegate { BuildTiles(found); });
            }
        }
        catch (Exception ex) { Status("Error: " + ex.Message); Log(ex.ToString()); }
    }

    // Polls a P2P status function until it returns 0 (ok), max `secs` seconds.
    bool WaitFor(string what, int secs, Func<int> check)
    {
        var end = DateTime.Now.AddSeconds(secs);
        int last = -99;
        while (DateTime.Now < end)
        {
            int s = check();
            if (s != last) { Log(what + ": estado " + s); last = s; }
            if (s == 0) return true;
            Status("Conectando: " + what + "...");
            Thread.Sleep(300);
        }
        Log(what + ": no llego a 0");
        return false;
    }

    static void WriteStr(IntPtr p, int off, string s, int max)
    {
        var b = Encoding.UTF8.GetBytes(s);
        for (int i = 0; i < b.Length && i < max - 1; i++) Marshal.WriteByte(p, off + i, b[i]);
    }

    // First run: ask for the NVR password, save it encrypted for this Windows user.
    static bool EnsurePassword()
    {
        if (File.Exists(PassFile)) return true;
        var f = new Form { Text = "Camaras - clave del NVR", Width = 380, Height = 150, FormBorderStyle = FormBorderStyle.FixedDialog, StartPosition = FormStartPosition.CenterScreen, MaximizeBox = false, MinimizeBox = false };
        var l = new Label { Text = "Clave del NVR (la misma que en DMSS):", Left = 12, Top = 12, Width = 340 };
        var t = new TextBox { Left = 12, Top = 36, Width = 340, UseSystemPasswordChar = true };
        var b = new Button { Text = "Guardar", Left = 272, Top = 68, Width = 80, DialogResult = DialogResult.OK };
        f.Controls.AddRange(new Control[] { l, t, b });
        f.AcceptButton = b;
        if (f.ShowDialog() != DialogResult.OK || t.Text == "") return false;
        Directory.CreateDirectory(Path.GetDirectoryName(PassFile));
        File.WriteAllBytes(PassFile, ProtectedData.Protect(Encoding.UTF8.GetBytes(t.Text), null, DataProtectionScope.CurrentUser));
        t.Text = "";
        return true;
    }

    [STAThread]
    static void Main()
    {
        Application.EnableVisualStyles();
        Log("---- inicio");
        if (!EnsurePassword()) return;
        Application.Run(new CamForm());
    }
}
