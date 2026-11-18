// Camaras: shows all cameras of a Dahua NVR full screen, through Dahua P2P (no open ports needed), nothing else.
// Uses the Dahua libraries that SmartPSS Lite installed:
//   P2PDll.dll  -> opens a local tunnel to the NVR by serial number (P2P)
//   dhnetsdk.dll -> logs in through that tunnel and plays live video into windows
// Click a camera: full screen; click again or Esc: all cameras. Esc with all cameras: close.
// Right click a camera: switch it between low quality (fast) and HD. The choice is saved.
// Only one copy runs: opening it again brings the running one to the front.
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
    // Moves a PTZ camera. Start with stop=false, stop the same command with stop=true.
    // Commands: 0 up, 1 down, 2 left, 3 right, 4 zoom in, 5 zoom out, 0x20 up-left, 0x21 up-right, 0x22 down-left, 0x23 down-right.
    // Speeds 1-8: p2 = speed (diagonals: p1 = vertical speed, p2 = horizontal speed).
    [DllImport("dhnetsdk.dll")] public static extern bool CLIENT_DHPTZControlEx2(long lLoginID, int nChannelID, uint cmd, int p1, int p2, int p3, bool stop, IntPtr p4);
    // Saves the current picture of a live video to a file (format 1 = JPEG).
    [DllImport("dhnetsdk.dll", CharSet = CharSet.Ansi)] public static extern bool CLIENT_CapturePicture(long hPlayHandle, string fileName);
    [DllImport("dhnetsdk.dll", CharSet = CharSet.Ansi)] public static extern bool CLIENT_CapturePictureEx(long hPlayHandle, string fileName, int format);
}

static class Win
{
    [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] public static extern short GetAsyncKeyState(int vKey);
    [DllImport("user32.dll")] public static extern IntPtr WindowFromPoint(Point p);
    [DllImport("user32.dll")] public static extern IntPtr GetAncestor(IntPtr hWnd, uint flags);
    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
    [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
    [DllImport("kernel32.dll")] public static extern uint SetThreadExecutionState(uint flags);
    [DllImport("user32.dll")] public static extern bool ShowWindowAsync(IntPtr hWnd, int cmd);   // async: does not hang on a frozen window
}

class CamForm : Form
{
    static string Dir = AppDomain.CurrentDomain.BaseDirectory;
    static string LogFile = Path.Combine(Dir, "camaras.log");
    static string PassFile = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData), "camaras", "nvr.bin");
    static Native.DisConnectCb keepCb = (a, b, c, d) => Log("desconectado del NVR");

    Dictionary<string, string> cfg = new Dictionary<string, string>();

    // One square on screen. Half: 0 = whole picture, 1 = top half, 2 = bottom half
    // (for a channel whose picture is two cameras stacked; it is played twice and each square shows one half),
    // 3 = panorama: both halves pasted into one wide picture.
    // A split channel is played ONCE: the SDK decodes into Video2 (a tiny panel nobody sees) and I draw each half,
    // or the panorama, into Video myself (see PanoView), so both halves always show the same moment.
    class Tile { public Panel Box, Video, Video2; public Label Tag; public int Ch, Half; public long Play, Play2, LastBytes; public bool PlayingHd; public DateTime LastData; public double Zoom = 1, PanX, PanY; public PanoView View; }

    // Panorama of a split channel, found when it is turned on (kept in panoramica.txt). Two modes:
    //  1 = simple: C1 = {s, dx, dy}. Lens B (bottom half) placed over lens A (top half): a point (x, y) of B lands on
    //      (dx + s*x, dy + s*y) of A. Units: A's width = 1 (both pictures are 16:9). Hard seam, no bending.
    //  2 = advanced: C2 = {f, k, yaw, pitch, roll, a0, a1, h0, h1}: lens shape and how B is turned from A (see Lens),
    //      shown on a cylinder (angle a0..a1, height h0..h1), soft seam.
    class PanoCal { public int Mode; public double[] C1, C2; }
    Dictionary<int, PanoCal> pano = new Dictionary<int, PanoCal>();
    bool panoBusy;
    static string PanoFile = Path.Combine(Dir, "panoramica.txt");
    volatile string panoMsg = "";
    const double LensH = 9.0 / 16;
    List<Tile> tiles = new List<Tile>();
    Tile big = null;
    Panel[] frame = new Panel[4];   // yellow frame around the camera a dragged camera will swap with
    static string OrderFile = Path.Combine(Dir, "orden.txt");
    static string Key(Tile t) { return t.Half == 0 ? t.Ch.ToString() : t.Ch + "." + t.Half; }
    Label status;
    long login = 0;
    static string CacheFile = Path.Combine(Dir, "canales.txt");
    // Channels shown in HD (main stream). The others use the low quality stream (sub stream), which is faster.
    static string HdFile = Path.Combine(Dir, "hd.txt");
    HashSet<int> hd = new HashSet<int>();
    static Mutex single;

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
        try { foreach (var c in File.ReadAllText(HdFile).Split(',')) { int ch; if (int.TryParse(c.Trim(), out ch)) hd.Add(ch); } } catch { }
        try
        {
            var inv = System.Globalization.CultureInfo.InvariantCulture;
            foreach (var line in File.ReadAllLines(PanoFile))
            {
                var f = line.Split(' ');
                Func<int, int, double[]> nums = (a, n) => { var r = new double[n]; for (int i = 0; i < n; i++) r[i] = double.Parse(f[a + i], inv); return r; };
                if (f.Length == 4) pano[int.Parse(f[0])] = new PanoCal { Mode = 1, C1 = nums(1, 3) };   // old file: simple mode
                else if (f.Length >= 5)
                {
                    var c = new PanoCal { Mode = int.Parse(f[1]), C1 = nums(2, 3) };
                    if (f.Length >= 14) c.C2 = nums(5, f.Length - 5);   // 9 numbers, then the fine bending (kept in mode 1 too)
                    if (c.Mode == 1 || c.C2 != null) pano[int.Parse(f[0])] = c;
                }
            }
        }
        catch { }
        bool wasDown = false, rightWas = false, escWas = false, pressOk = false, dragging = false;
        Tile ptzTile = null, panTile = null; double panX0 = 0, panY0 = 0;
        int zoomWas = 0; DateTime zoomStep = DateTime.MinValue;
        Point pressAt = Point.Empty;
        Tile dragFrom = null;
        for (int i = 0; i < 4; i++) { frame[i] = new Panel { BackColor = Color.Yellow, Visible = false }; Controls.Add(frame[i]); }
        var poll = new System.Windows.Forms.Timer { Interval = 40 };
        poll.Tick += (s, e) =>
        {
            // Left mouse button, only when the pointer is over this app (any of its windows, also the video ones).
            // Press and release in the same place = click. Press, move and release over another camera = swap them.
            bool down = (Win.GetAsyncKeyState(0x01) & 0x8001) != 0;   // 0x0001 = pressed since last check, catches short clicks
            Point pos = PointToClient(Cursor.Position);
            if (down && !wasDown)
            {
                pressOk = Win.GetAncestor(Win.WindowFromPoint(Cursor.Position), 2) == Handle;   // 2 = the top window
                pressAt = pos; dragging = false;
                dragFrom = pressOk && big == null ? TileAt(pos) : null;
                ptzTile = pressOk && big != null && TileAt(pos) == big && CanPtz(big) ? big : null;
                panTile = pressOk && big != null && TileAt(pos) == big && !CanPtz(big) && big.Zoom > 1 ? big : null;
                if (panTile != null) { panX0 = panTile.PanX; panY0 = panTile.PanY; }
            }
            else if (down && panTile != null)
            {
                // Zoomed camera without PTZ: dragging moves the picture with the mouse.
                if (!dragging && (Math.Abs(pos.X - pressAt.X) > 20 || Math.Abs(pos.Y - pressAt.Y) > 20)) dragging = true;
                if (dragging)
                {
                    int bw = panTile.Box.ClientSize.Width, bh = panTile.Box.ClientSize.Height; double z = panTile.Zoom;
                    panTile.PanX = Math.Max(-1, Math.Min(1, panX0 - (pos.X - pressAt.X) / (bw * (z - 1) / 2)));
                    panTile.PanY = Math.Max(-1, Math.Min(1, panY0 - (pos.Y - pressAt.Y) / (bh * (z - 1) / 2)));
                    DoLayout();
                }
            }
            else if (down && ptzTile != null)
            {
                // Full screen camera: dragging moves a PTZ camera like a joystick (direction and speed from the drag).
                if (!dragging && (Math.Abs(pos.X - pressAt.X) > 20 || Math.Abs(pos.Y - pressAt.Y) > 20)) dragging = true;
                if (dragging) PtzDrag(ptzTile.Ch, pos.X - pressAt.X, pos.Y - pressAt.Y);
            }
            else if (down && dragFrom != null)
            {
                if (!dragging && (Math.Abs(pos.X - pressAt.X) > 20 || Math.Abs(pos.Y - pressAt.Y) > 20)) dragging = true;
                if (dragging) { var to = TileAt(pos); ShowFrame(to != null && to != dragFrom ? to.Box.Bounds : Rectangle.Empty); }
            }
            else if (!down && wasDown)
            {
                ShowFrame(Rectangle.Empty);
                if (panTile != null && dragging) { }
                else if (ptzTile != null && dragging) PtzStop();
                else if (dragging) { var to = TileAt(pos); if (to != null && to != dragFrom) Swap(dragFrom, to); }
                else if (pressOk) ClickAt(pressAt);
                dragFrom = null; ptzTile = null; panTile = null; dragging = false;
            }
            wasDown = down;
            bool right = (Win.GetAsyncKeyState(0x02) & 0x8001) != 0;   // right mouse button
            if (right && !rightWas && Win.GetAncestor(Win.WindowFromPoint(Cursor.Position), 2) == Handle) RightClickAt(PointToClient(Cursor.Position));
            rightWas = right;
            // Esc: only when this app is in front.
            if (Win.GetForegroundWindow() != Handle) { escWas = false; return; }
            // + / - held down: zoom the full screen PTZ camera in / out.
            bool zin = (Win.GetAsyncKeyState(0x6B) & 0x8000) != 0 || (Win.GetAsyncKeyState(0xBB) & 0x8000) != 0;
            bool zout = (Win.GetAsyncKeyState(0x6D) & 0x8000) != 0 || (Win.GetAsyncKeyState(0xBD) & 0x8000) != 0;
            int zoom = big == null ? 0 : zin ? 1 : zout ? -1 : 0;
            if (zoom != 0 && (zoom != zoomWas || (DateTime.Now - zoomStep).TotalMilliseconds > 250)) { ZoomStep(big, zoom); zoomStep = DateTime.Now; }
            zoomWas = zoom;
            bool esc = (Win.GetAsyncKeyState(0x1B) & 0x8000) != 0;
            if (esc && !escWas) { if (big != null) { big = null; DoLayout(); } else Close(); }
            escWas = esc;
        };
        poll.Start();

        // While the app is in front: no sleep and no screen off. In the background: Windows decides as usual.
        var awake = new System.Windows.Forms.Timer { Interval = 10000 };
        awake.Tick += (s, e) => KeepAwake();
        awake.Start();
        Shown += (s, e) => KeepAwake();

        var watchdog = new System.Windows.Forms.Timer { Interval = 5000 };
        watchdog.Tick += (s, e) => Watchdog();
        watchdog.Start();
        // After the PC wakes up the tunnel and login are gone: start again from zero.
        Microsoft.Win32.SystemEvents.PowerModeChanged += (s, e) =>
        {
            if (e.Mode == Microsoft.Win32.PowerModes.Resume) BeginInvoke((MethodInvoker)delegate { Restart("la PC desperto"); });
        };

        status = new Label { Text = "Conectando...", ForeColor = Color.Gray, AutoSize = true, Location = new Point(20, 20), Font = new Font("Segoe UI", 16) };
        Controls.Add(status);
        Resize += (s, e) => DoLayout();
        Shown += (s, e) => new Thread(Connect) { IsBackground = true }.Start();
        Shown += (s, e) => StartRemote();
        // Show the address of the phone remote on screen (it can change, so it is refreshed).
        var ips = LocalIps();
        if (ips.Count > 0 && C("puerto_web", "8080") != "0")
        {
            var addr = new Label { Text = "http://" + ips[0] + ":" + C("puerto_web", "8080"), ForeColor = Color.White, BackColor = Color.FromArgb(40, 40, 40), AutoSize = true, Font = new Font("Segoe UI", 12) };
            addr.Font = new Font("Segoe UI", 9); addr.ForeColor = Color.Silver;
            Controls.Add(addr);
            addrLabel = addr;
            // QR code of the same address, above it: point the phone's camera at it to open the remote.
            qrBox = new PictureBox { SizeMode = PictureBoxSizeMode.AutoSize };
            Controls.Add(qrBox);
            SetQr("http://" + ips[0] + ":" + C("puerto_web", "8080"));
            Shown += (s, e) => PlaceAddr();
            Resize += (s, e) => PlaceAddr();
            // The PC's address can change (DHCP): check it every 30 s.
            var refresh = new System.Windows.Forms.Timer { Interval = 30000 };
            refresh.Tick += (s, e) => { var l = LocalIps(); if (l.Count > 0) { addr.Text = "http://" + l[0] + ":" + C("puerto_web", "8080"); SetQr("http://" + l[0] + ":" + C("puerto_web", "8080")); PlaceAddr(); } };
            refresh.Start();
        }
    }

    // Channels whose camera can really be moved (PTZ). The recorder sends every PTZ command to its one PTZ camera,
    // whatever channel is asked, so only these channels get commands. Setting "ptz" in camaras.ini, e.g. ptz=2.
    // A split camera can list one lens only: "2.2" = bottom half of channel 2 ("2" = both halves).
    bool CanPtz(Tile t) { if (t.Half == 3) return false; foreach (var c in C("ptz", "").Split(',')) { string s = c.Trim(); if (s.Length > 0 && (s == Key(t) || s == t.Ch.ToString())) return true; } return false; }

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
        foreach (var t in tiles) { StopPlay(t); Controls.Remove(t.Box); t.Box.Dispose(); }
        tiles.Clear();
        big = null;
        var split = SplitChannels();
        foreach (var ch in chans)
            foreach (var half in !split.Contains(ch) ? new[] { 0 } : pano.ContainsKey(ch) ? new[] { 3 } : new[] { 1, 2 })
            {
                var t = new Tile { Ch = ch, Half = half };
                t.Box = new Panel { BackColor = Color.Black };
                t.Video = new Panel { BackColor = Color.Black };
                t.Box.Controls.Add(t.Video);
                if (half != 0)
                {
                    t.Video2 = new Panel { BackColor = Color.Black, Bounds = new Rectangle(0, 0, 2, 2) };
                    t.Box.Controls.Add(t.Video2);
                    t.Video.BringToFront();
                    t.View = new PanoView { Half = half == 3 ? 0 : half, Cal = half == 3 ? pano[ch] : null, Target = t.Video.Handle };
                }
                t.Tag = new Label { Text = "HD", ForeColor = Color.White, BackColor = Color.FromArgb(200, 0, 0), AutoSize = true, Font = new Font("Segoe UI", 18, FontStyle.Bold), Location = new Point(24, 24), Visible = hd.Contains(ch) };
                t.Box.Controls.Add(t.Tag);
                t.Tag.BringToFront();
                tiles.Add(t);
                Controls.Add(t.Box);
            }
        // Order saved by drag and drop; cameras not in the file go at the end.
        var order = new List<string>();
        try { order.AddRange(File.ReadAllText(OrderFile).Split(',')); } catch { }
        var orig = new List<Tile>(tiles);
        // The panorama takes the place of its first lens, and back.
        Func<Tile, int> pos = t =>
        {
            int i = order.IndexOf(Key(t));
            if (i < 0 && t.Half == 3) i = order.IndexOf(t.Ch + ".1");
            if (i < 0 && (t.Half == 1 || t.Half == 2)) i = order.IndexOf(t.Ch + ".3");
            return i;
        };
        tiles.Sort((a, b) =>
        {
            int ia = pos(a), ib = pos(b);
            if (ia < 0) ia = 1000 + orig.IndexOf(a);
            if (ib < 0) ib = 1000 + orig.IndexOf(b);
            return ia.CompareTo(ib);
        });
        status.Visible = tiles.Count == 0;
        DoLayout();
        foreach (var t in tiles) if (t.Play == 0) PlayTile(t);   // the second half of a split channel already got its stream
    }

    // (Re)starts the video of one square and counts its bytes, so the watchdog can see if it stops.
    void PlayTile(Tile t)
    {
        // A split channel: one stream for all its squares (both halves, or the panorama).
        var group = new List<Tile>();
        if (t.View != null) { foreach (var u in tiles) if (u.Ch == t.Ch && u.View != null) group.Add(u); }
        else group.Add(t);
        foreach (var u in group) StopPlay(u);
        bool wantHd = WantHd(t);
        long h = Native.CLIENT_RealPlayEx(login, t.Ch, (t.View != null ? t.Video2 : t.Video).Handle, wantHd ? 0 : int.Parse(C("flujo", "3")));   // 0 = main stream (HD)
        foreach (var u in group) { u.Play = h; u.PlayingHd = wantHd; u.LastBytes = 0; u.LastData = DateTime.Now; }
        UpdateState();   // new stream number for the phone pictures
        // The SDK reuses the numbers of stopped streams: start counting this one from zero.
        lock (bytesPerPlay) bytesPerPlay.Remove(h);
        if (h != 0 && t.View != null)
        {
            // Also ask for the decoded frames (flag 4): I draw them myself.
            var vs = new List<PanoView>(); foreach (var u in group) vs.Add(u.View);
            lock (views) views[h] = vs.ToArray();
            Native.CLIENT_SetRealDataCallBackEx2(h, panoCb, IntPtr.Zero, 1 | 4);
        }
        else if (h != 0) Native.CLIENT_SetRealDataCallBackEx2(h, dataCb, IntPtr.Zero, 1);
        string what = t.Half == 0 ? "" : t.Half == 3 ? " (panoramica " + t.View.Cal.Mode + ")" : group.Count > 1 ? " (las dos mitades)" : t.Half == 1 ? " (arriba)" : " (abajo)";
        Log("RealPlay canal " + t.Ch + what + (wantHd ? " HD" : "") + " -> " + h + (h == 0 ? " err 0x" + Native.CLIENT_GetLastError().ToString("X") : ""));
    }

    // Stops a square's stream (squares sharing it lose it too).
    void StopPlay(Tile t)
    {
        foreach (var h in new[] { t.Play, t.Play2 })
            if (h != 0)
            {
                Native.CLIENT_StopRealPlayEx(h);
                lock (bytesPerPlay) bytesPerPlay.Remove(h);
                lock (views) views.Remove(h);
                foreach (var u in tiles) if (u.Play == h) u.Play = 0;
            }
        t.Play = t.Play2 = 0;
    }

    static long BytesOf(long play) { long b = 0; if (play != 0) lock (bytesPerPlay) bytesPerPlay.TryGetValue(play, out b); return b; }

    // Watchdog, every 5 s: a square without new video for 20 s gets its video restarted.
    // If no square got video for 90 s (tunnel or login lost), the whole app starts again.
    DateTime lastAnyData = DateTime.Now;
    // Channels that sent video since the app started (a square built again a moment ago has no bytes yet).
    HashSet<int> gotVideo = new HashSet<int>();
    void Watchdog()
    {
        if (login == 0 || tiles.Count == 0) return;
        foreach (var t in tiles)
        {
            long b = BytesOf(t.Play) + BytesOf(t.Play2);
            if (b > t.LastBytes) { t.LastBytes = b; t.LastData = DateTime.Now; lastAnyData = DateTime.Now; gotVideo.Add(t.Ch); }
            else if ((DateTime.Now - t.LastData).TotalSeconds > 20) { Log("sin video 20 s, reinicio canal " + t.Ch); PlayTile(t); }
        }
        if ((DateTime.Now - lastAnyData).TotalSeconds > 90) Restart("ninguna camara con video por 90 s");
    }

    // Starts the app again from zero (new P2P tunnel, new login).
    // Wait before the next try after a failed connection: 2 s, then double each time, up to 15 s.
    // A short cut comes back almost at once; a long one does not flood the Dahua server.
    static int retryMs = 2000;

    void Restart(string why) { Restart(why, 2000); }
    void Restart(string why, int nextWaitMs)
    {
        Log("reinicio la app: " + why);
        try { single.ReleaseMutex(); } catch { }   // so the new copy can start
        System.Diagnostics.Process.Start(Application.ExecutablePath, "--espera " + nextWaitMs);
        HardExit();
    }

    // Ends this process at once. A normal exit lets the Dahua libraries clean up, and that can hang forever
    // (seen after the PC woke up: the old copy stayed as a frozen window that could not even be killed).
    static void HardExit()
    {
        Log("fin");
        System.Diagnostics.Process.GetCurrentProcess().Kill();
    }

    void DoLayout()
    {
        UpdateState();
        int n = tiles.Count;
        if (n == 0) return;
        int W = ClientSize.Width, H = ClientSize.Height;
        var units = new int[n]; for (int i = 0; i < n; i++) units[i] = tiles[i].Half == 3 ? 2 : 1;   // a panorama is 2 cells wide
        var cells = big == null ? Arrange(units, W, H) : ArrangeBig(n, tiles.IndexOf(big), W, H);
        for (int i = 0; i < n; i++)
        {
            var t = tiles[i];
            t.Box.Visible = true;
            var c = cells[i];
            t.Box.Bounds = new Rectangle(c.X + 1, c.Y + 1, c.Width - 2, c.Height - 2);
            // HD tag on every HD camera; smaller on the small pictures.
            bool small = big != null && big != t;
            t.Tag.Visible = hd.Contains(t.Ch);
            if (t.Tag.Font.Size != (small ? 8f : 18f)) t.Tag.Font = new Font("Segoe UI", small ? 8f : 18f, FontStyle.Bold);
            t.Tag.Location = small ? new Point(4, 4) : new Point(24, 24);
            if (small) t.Box.BringToFront();   // small pictures float over the big one
            if (addrLabel != null) addrLabel.BringToFront();
            if (qrBox != null) qrBox.BringToFront();
            int bw = t.Box.ClientSize.Width, bh = t.Box.ClientSize.Height;
            if (t != big) { t.Zoom = 1; t.PanX = t.PanY = 0; }
            if (t.View != null) { LayoutPano(t, bw, bh); continue; }
            // A half square holds a video panel twice as tall, shifted so only its half shows.
            var vr = t.Half == 0 ? new Rectangle(0, 0, bw, bh) : new Rectangle(0, t.Half == 1 ? 0 : -bh, bw, bh * 2);
            t.Video.Bounds = Zoomed(t, vr, bw, bh);
        }
    }

    // Digital zoom (full screen only): the rectangle grows around the middle of the square; the square cuts it.
    static Rectangle Zoomed(Tile t, Rectangle vr, int bw, int bh)
    {
        if (t.Zoom <= 1) { t.PanX = t.PanY = 0; return vr; }
        double cx = bw / 2.0, cy = bh / 2.0, z = t.Zoom;
        // Digital pan: PanX/PanY from -1 to 1 move the cut-out to the left/top or right/bottom edge.
        double sx = -t.PanX * bw * (z - 1) / 2, sy = -t.PanY * bh * (z - 1) / 2;
        return new Rectangle((int)(cx - (cx - vr.X) * z + sx), (int)(cy - (cy - vr.Y) * z + sy), (int)(vr.Width * z), (int)(vr.Height * z));
    }

    // Panorama geometry in units of lens A's width: the whole width of both lenses, and only the rows both have.
    // The seam is in the middle of the part both lenses see (each lens bends less near its own middle).
    static void PanoBox(double[] c, out double x0, out double x1, out double y0, out double y1, out double seam)
    {
        double s = c[0], dx = c[1], dy = c[2];
        x0 = Math.Min(0, dx); x1 = Math.Max(1, dx + s);
        y0 = Math.Max(0, dy); y1 = Math.Min(LensH, dy + s * LensH);
        seam = (Math.Max(0, dx) + Math.Min(1, dx + s)) / 2;
    }

    // Square drawn by me (half or panorama): the picture keeps its shape, fills the square as much as it can, and zooms like the others.
    void LayoutPano(Tile t, int bw, int bh)
    {
        if (t.View == null) return;
        double aspect = t.View.Aspect(), fw = Math.Min(bw, bh * aspect), fh = fw / aspect;
        var vr = Zoomed(t, new Rectangle((int)((bw - fw) / 2), (int)((bh - fh) / 2), (int)fw, (int)fh), bw, bh);
        t.Video.Bounds = vr;
        t.View.TW = vr.Width; t.View.TH = vr.Height;
    }

    // One camera full screen (biggest 16:9 rectangle, centered). The others float over its bottom edge as small
    // pictures (10% of the screen width each, in a centered row), so the big one keeps its full size.
    static List<Rectangle> ArrangeBig(int n, int bigIdx, int W, int H)
    {
        double fs = Math.Min((double)W / 16, (double)H / 9);
        double gw = fs * 16, gh = fs * 9, x0 = (W - gw) / 2, y0 = (H - gh) / 2;
        int m = n - 1;
        double tw = Math.Min(W * 0.10, m > 0 ? (gw - 20) / m : 0), th = tw * 9 / 16, gap = 10;
        double tx = (W - m * tw) / 2, ty = y0 + gh - th - gap;
        var list = new List<Rectangle>();
        int k = 0;
        for (int i = 0; i < n; i++)
        {
            if (i == bigIdx) { list.Add(new Rectangle((int)x0, (int)y0, (int)gw, (int)gh)); continue; }
            list.Add(new Rectangle((int)(tx + k * tw), (int)ty, (int)tw, (int)th));
            k++;
        }
        return list;
    }

    // Places the pictures (no stretching) so that together they cover as much of the screen as possible.
    // u[i] = how many 16:9 cells wide picture i is (1, or 2 for a panorama).
    // Two kinds of layouts are tried and the bigger one wins:
    //  - a grid of equal cells, c cells wide; a 2-cell picture that does not fit at the end of a row starts the next row,
    //  - rows with different counts (e.g. 5 = 2 big on top + 3 smaller below), each row as wide as the screen.
    // The result is centered.
    static List<Rectangle> Arrange(int[] u, int W, int H)
    {
        int n = u.Length, total = 0, widest = 1;
        foreach (var x in u) { total += x; widest = Math.Max(widest, x); }
        List<Rectangle> best = null; double bestArea = -1;
        Action<List<Rectangle>> consider = list =>
        {
            double a = 0; foreach (var r in list) a += (double)r.Width * r.Height;
            if (a > bestArea + 1) { bestArea = a; best = list; }
        };
        for (int c = widest; c <= total; c++)
        {
            var row = new int[n]; var col = new int[n]; int rr = 0, used = 0;
            for (int i = 0; i < n; i++) { if (used + u[i] > c) { rr++; used = 0; } row[i] = rr; col[i] = used; used += u[i]; }
            int rows = rr + 1;
            double sc = Math.Min((double)W / c / 16, (double)H / rows / 9);
            int w = (int)(sc * 16), h = (int)(sc * 9), x0 = (W - c * w) / 2, y0 = (H - rows * h) / 2;
            var list = new List<Rectangle>();
            for (int i = 0; i < n; i++) list.Add(new Rectangle(x0 + col[i] * w, y0 + row[i] * h, u[i] * w, h));
            consider(list);
        }
        for (int r = 1; r <= n; r++)
        {
            int baseK = n / r, extra = n % r;
            var counts = new int[r];
            for (int i = 0; i < r; i++) counts[i] = baseK + (i >= r - extra ? 1 : 0);   // fewer (bigger) on top
            var rowUnits = new int[r]; int k0 = 0;
            for (int i = 0; i < r; i++) { for (int j = 0; j < counts[i]; j++) rowUnits[i] += u[k0 + j]; k0 += counts[i]; }
            double sum = 0; foreach (var ru in rowUnits) sum += (double)W / ru * 9 / 16;
            double f = Math.Min(1, H / sum);
            double y = (H - sum * f) / 2;
            var list = new List<Rectangle>(); k0 = 0;
            for (int i = 0; i < r; i++)
            {
                double cw = f * W / rowUnits[i], h = cw * 9 / 16, x = (W - rowUnits[i] * cw) / 2;
                for (int j = 0; j < counts[i]; j++) { int k = k0 + j; list.Add(new Rectangle((int)x, (int)y, (int)(u[k] * cw), (int)h)); x += u[k] * cw; }
                k0 += counts[i]; y += h;
            }
            consider(list);
        }
        return best;
    }

    // The camera under a point. The small pictures lie on top of the big one, so they are checked first.
    Tile TileAt(Point p)
    {
        foreach (var t in tiles) if (t != big && t.Box.Visible && t.Box.Bounds.Contains(p)) return t;
        return big != null && big.Box.Bounds.Contains(p) ? big : null;
    }

    void ShowFrame(Rectangle r)
    {
        int w = 6;
        var parts = new[] { new Rectangle(r.X, r.Y, r.Width, w), new Rectangle(r.X, r.Bottom - w, r.Width, w), new Rectangle(r.X, r.Y, w, r.Height), new Rectangle(r.Right - w, r.Y, w, r.Height) };
        for (int i = 0; i < 4; i++) { frame[i].Visible = !r.IsEmpty; if (!r.IsEmpty) { frame[i].Bounds = parts[i]; frame[i].BringToFront(); } }
    }

    // Drag and drop: the two cameras swap places (the video keeps running) and the order is saved.
    void Swap(Tile a, Tile b)
    {
        int ia = tiles.IndexOf(a), ib = tiles.IndexOf(b);
        tiles[ia] = b; tiles[ib] = a;
        DoLayout();
        var keys = new List<string>(); foreach (var t in tiles) keys.Add(Key(t));
        try { File.WriteAllText(OrderFile, string.Join(",", keys)); } catch { }
        Log("orden: " + string.Join(",", keys));
    }

    // Digital zoom of the full screen camera: one step = x1.25 (in) or /1.25 (out), from 1x to 4x.
    void ZoomStep(Tile t, int dir)
    {
        if (t == null || t != big) return;
        t.Zoom = Math.Max(1, Math.Min(4, dir > 0 ? t.Zoom * 1.25 : t.Zoom / 1.25));
        if (t.Zoom < 1.01) t.Zoom = 1;
        DoLayout();
    }

    // ---- PTZ: commands go to a worker thread in order, so the screen never waits for the network. ----
    System.Collections.Concurrent.BlockingCollection<Action> ptzQ;
    uint ptzCmd; int ptzCh = -1, ptzP1, ptzP2; bool ptzOn;
    void PtzSend(Action a)
    {
        if (ptzQ == null)
        {
            ptzQ = new System.Collections.Concurrent.BlockingCollection<Action>();
            new Thread(() => { foreach (var x in ptzQ.GetConsumingEnumerable()) try { x(); } catch (Exception ex) { Log("ptz: " + ex.Message); } }) { IsBackground = true }.Start();
        }
        ptzQ.Add(a);
    }
    void PtzStart(int ch, uint cmd, int p1, int p2)
    {
        if (ptzOn && ch == ptzCh && cmd == ptzCmd && p1 == ptzP1 && p2 == ptzP2) return;
        if (ptzOn) PtzStop();
        ptzOn = true; ptzCh = ch; ptzCmd = cmd; ptzP1 = p1; ptzP2 = p2;
        long lg = login;
        PtzSend(() => { bool ok = Native.CLIENT_DHPTZControlEx2(lg, ch, cmd, p1, p2, 0, false, IntPtr.Zero); Log("ptz canal " + ch + " cmd 0x" + cmd.ToString("X") + " vel " + p1 + "/" + p2 + (ok ? " ok" : " error 0x" + Native.CLIENT_GetLastError().ToString("X"))); });
    }
    void PtzStop()
    {
        PulseOff();
        if (!ptzOn) return;
        ptzOn = false;
        long lg = login; int ch = ptzCh; uint cmd = ptzCmd; int p1 = ptzP1, p2 = ptzP2;
        PtzSend(() => Native.CLIENT_DHPTZControlEx2(lg, ch, cmd, p1, p2, 0, true, IntPtr.Zero));
    }
    // Drag of (dx, dy) pixels from the press point: 8 directions, speed 1-8 grows with the distance.
    void PtzDrag(int ch, int dx, int dy)
    {
        // Settings: ptz_invertir_pan=1 turns left/right around; ptz_velocidad=50 = half speed (percent).
        if (C("ptz_invertir_pan", "0") == "1") dx = -dx;
        double ang = Math.Atan2(-dy, dx) * 180 / Math.PI;   // 0 = right, 90 = up
        int sector = (int)Math.Round(((ang + 360) % 360) / 45) % 8;
        int speed0 = Math.Max(1, Math.Min(8, (int)(Math.Sqrt(dx * dx + dy * dy) / 40)));
        double want = speed0 * int.Parse(C("ptz_velocidad", "100")) / 100.0;   // may be below 1, the slowest real speed
        int speed = Math.Max(1, (int)Math.Round(want));
        uint[] cmds = { 3, 0x21, 0, 0x20, 2, 0x22, 1, 0x23 };   // right, up-right, up, up-left, left, down-left, down, down-right
        uint cmd = cmds[sector];
        bool diag = cmd >= 0x20;
        if (want >= 1) { PulseOff(); PtzStart(ch, cmd, diag ? speed : 0, speed); return; }
        // Slower than speed 1: move at speed 1 in short pulses (on for want x 400 ms of every 400 ms, at least 60 ms).
        if (ptzOn) PtzStop();
        pulseCh = ch; pulseCmd = cmd; pulseOnMs = Math.Max(60, (int)(want * 400));
        if (pulseThread == null)
        {
            pulseRun = true;
            pulseThread = new Thread(() =>
            {
                long lg = login;
                while (pulseRun)
                {
                    int c = pulseCh; uint k = pulseCmd; int on = pulseOnMs; int p1 = k >= 0x20 ? 1 : 0;
                    Native.CLIENT_DHPTZControlEx2(lg, c, k, p1, 1, 0, false, IntPtr.Zero);
                    Thread.Sleep(on);
                    Native.CLIENT_DHPTZControlEx2(lg, c, k, p1, 1, 0, true, IntPtr.Zero);
                    Thread.Sleep(Math.Max(0, 400 - on));
                }
            }) { IsBackground = true };
            pulseThread.Start();
            Log("ptz canal " + ch + " pulsos de " + pulseOnMs + " ms cada 400 ms");
        }
    }
    volatile bool pulseRun; volatile int pulseCh, pulseOnMs; volatile uint pulseCmd; Thread pulseThread;
    void PulseOff() { pulseRun = false; pulseThread = null; }

    // Click on a camera: show it alone, full screen. Click again: back to all cameras.
    void ClickAt(Point p)
    {
        var t = TileAt(p);
        if (big != null && (t == null || t == big)) big = null;   // click on the big camera: back to all cameras
        else if (t != null) big = t;                                // click on a camera (also a small one on the side): make it big
        DoLayout();
    }

    bool WantHd(Tile t) { return hd.Contains(t.Ch); }

    // Right click on a camera: switch its channel between low quality and HD (both halves of a split channel).
    void RightClickAt(Point p)
    {
        foreach (var t in tiles)
        {
            if (!t.Box.Visible || !t.Box.Bounds.Contains(p)) continue;
            ToggleHd(t);
            return;
        }
    }

    // ---- Remote control for a phone on the same network: a tiny web page (port `puerto_web`, default 8080). ----
    // Only answers addresses of the local network. Tap a camera to make it big, tap again for all; HD button per camera.
    bool awakeOn;
    void KeepAwake()
    {
        bool front = Win.GetForegroundWindow() == Handle;
        // 0x80000000 = continuous, 0x1 = system needed, 0x2 = display needed. Only "continuous" = normal again.
        Win.SetThreadExecutionState(front ? 0x80000003u : 0x80000000u);   // timer runs on the UI thread, so the state stays with it
        if (front != awakeOn) { awakeOn = front; Log(front ? "pantalla siempre encendida (app al frente)" : "pantalla normal (app atras)"); }
    }

    Label addrLabel;
    // The phone remote address stays on screen, small, in the bottom right corner, over the video.
    void PlaceAddr()
    {
        if (addrLabel == null) return;
        addrLabel.Location = new Point(ClientSize.Width - addrLabel.Width - 6, ClientSize.Height - addrLabel.Height - 4);
        addrLabel.BringToFront();
        if (qrBox != null) { qrBox.Location = new Point(6, ClientSize.Height - qrBox.Height - 4); qrBox.BringToFront(); }   // bottom left corner
    }

    PictureBox qrBox;
    string qrText;
    void SetQr(string text)
    {
        if (qrBox == null || text == qrText) return;
        qrText = text;
        try { var old = qrBox.Image; qrBox.Image = Qr.ToBitmap(Qr.Encode(text), 2); if (old != null) old.Dispose(); }
        catch (Exception ex) { Log("qr: " + ex.Message); }
    }

    void ToggleHd(Tile t) { SetHd(t, !hd.Contains(t.Ch)); }

    // What the phone page shows, kept ready so a request never has to wait for the screen thread.
    volatile string stateJson = "{\"big\":-1,\"cams\":[]}";
    volatile long[] playHandles = new long[0];
    volatile int[] halves = new int[0];
    volatile PanoView[] viewOf = new PanoView[0];
    int PanoMode() { int m = 0; foreach (var c in pano.Values) m = Math.Max(m, c.Mode); return m; }
    void UpdateState()
    {
        var sb = new StringBuilder("{\"big\":" + (big == null ? -1 : tiles.IndexOf(big)) + ",\"split\":" + (SplitChannels().Count > 0 ? "true" : "false")
            + ",\"pano\":" + PanoMode() + ",\"msg\":\"" + panoMsg.Replace("\"", "'") + "\",\"cams\":[");
        for (int k = 0; k < tiles.Count; k++) sb.Append((k > 0 ? "," : "") + "{\"k\":\"" + Key(tiles[k]) + "\",\"hd\":" + (hd.Contains(tiles[k].Ch) ? "true" : "false") + ",\"ptz\":" + (CanPtz(tiles[k]) ? "true" : "false") + "}");
        stateJson = sb.Append("]}").ToString();
        var ph = new long[tiles.Count]; var hv = new int[tiles.Count]; var vw = new PanoView[tiles.Count];
        for (int k = 0; k < tiles.Count; k++) { ph[k] = tiles[k].Play; hv[k] = tiles[k].Half; vw[k] = tiles[k].View; }
        playHandles = ph; halves = hv; viewOf = vw;
    }

    // Sets HD on or off for the channel of a camera. Doing it twice does nothing more (the phone may repeat a request).
    // The tag and the answer change at once; the video restarts right after (that takes 1-2 s).
    void SetHd(Tile t, bool on)
    {
        int ch = t.Ch;
        if (hd.Contains(ch) == on) return;
        if (on) hd.Add(ch); else hd.Remove(ch);
        try { File.WriteAllText(HdFile, string.Join(",", hd)); } catch { }
        DoLayout();   // shows or hides the HD tag
        BeginInvoke((MethodInvoker)delegate { foreach (var u in tiles) if (u.Ch == ch && u.PlayingHd != hd.Contains(ch)) PlayTile(u); });
    }

    // Puts the cameras in the given order (keys like "0", "2.1"). Unknown keys are ignored, missing cameras go last.
    void SetOrder(string[] keys)
    {
        var old = new List<Tile>(tiles);
        tiles.Sort((a, b) =>
        {
            int ia = Array.IndexOf(keys, Key(a)), ib = Array.IndexOf(keys, Key(b));
            if (ia < 0) ia = 1000 + old.IndexOf(a);
            if (ib < 0) ib = 1000 + old.IndexOf(b);
            return ia.CompareTo(ib);
        });
        DoLayout();
        var k2 = new List<string>(); foreach (var t in tiles) k2.Add(Key(t));
        try { File.WriteAllText(OrderFile, string.Join(",", k2)); } catch { }
    }

    static bool IsLocal(System.Net.IPAddress a)
    {
        if (System.Net.IPAddress.IsLoopback(a)) return true;
        var b = a.GetAddressBytes();
        return b.Length == 4 && (b[0] == 10 || (b[0] == 192 && b[1] == 168) || (b[0] == 172 && b[1] >= 16 && b[1] <= 31) || (b[0] == 169 && b[1] == 254));
    }

    static List<string> LocalIps()
    {
        var l = new List<string>();
        try { foreach (var a in System.Net.Dns.GetHostAddresses(System.Net.Dns.GetHostName())) if (a.AddressFamily == System.Net.Sockets.AddressFamily.InterNetwork && !System.Net.IPAddress.IsLoopback(a)) l.Add(a.ToString()); } catch { }
        return l;
    }

    void StartRemote()
    {
        int port = int.Parse(C("puerto_web", "8080"));
        if (port <= 0) return;
        var srv = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Any, port);
        try { srv.Start(); } catch (Exception ex) { Log("web: no pude abrir el puerto " + port + ": " + ex.Message); return; }
        Log("web: control remoto en puerto " + port + " (" + string.Join(", ", LocalIps()) + ")");
        new Thread(() =>
        {
            while (true)
            {
                try
                {
                    var c = srv.AcceptTcpClient();
                    new Thread(() => Serve(c)) { IsBackground = true }.Start();
                }
                catch (Exception ex) { Log("web: " + ex.Message); Thread.Sleep(1000); }
            }
        }) { IsBackground = true }.Start();
    }

    // One connection can carry many requests (keep-alive). On the PC's Wi-Fi, opening a new connection often
    // fails, but an open one works fine, so the phone page keeps reusing the same connections.
    void Serve(System.Net.Sockets.TcpClient c)
    {
        using (c)
        {
            try
            {
                c.ReceiveTimeout = 120000; c.SendTimeout = 10000;   // an idle connection stays open 2 minutes
                var remote = ((System.Net.IPEndPoint)c.Client.RemoteEndPoint).Address;
                var st = c.GetStream();
                var rd = new StreamReader(st, Encoding.ASCII);
                while (true)
                {
                    string req = rd.ReadLine();
                    if (req == null) return;            // the phone closed the connection
                    if (req == "") continue;
                    bool close = false;
                    string h, wsKey = null;
                    while (!string.IsNullOrEmpty(h = rd.ReadLine()))
                    {
                        string hl = h.ToLower();
                        if (hl.StartsWith("connection:") && hl.Contains("close")) close = true;
                        if (hl.StartsWith("sec-websocket-key:")) wsKey = h.Substring(18).Trim();
                    }
                    if (!IsLocal(remote)) { Send(st, "403 Forbidden", "text/plain", Encoding.ASCII.GetBytes("solo red local")); return; }
                    if (wsKey != null && req.StartsWith("GET /ws")) { c.ReceiveTimeout = 30000; RunWs(st, wsKey); return; }
                    HandleRequest(st, req);
                    if (close) return;
                }
            }
            catch (IOException) { }   // idle timeout or the phone went away
            catch (Exception ex) { Log("web: " + ex.Message); }
        }
    }

    // ---- WebSocket: ONE connection that stays open carries everything (buttons, state, pictures). ----
    // On the PC's Wi-Fi, opening new connections is what fails while the cameras stream; an open one works fine.
    void RunWs(Stream st, string key)
    {
        string accept = Convert.ToBase64String(System.Security.Cryptography.SHA1.Create().ComputeHash(Encoding.ASCII.GetBytes(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")));
        var hs = Encoding.ASCII.GetBytes("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n");
        st.Write(hs, 0, hs.Length);
        var wlock = new object();
        bool alive = true;
        Action<int, byte[]> frame = (op, data) =>
        {
            lock (wlock)
            {
                var head = new List<byte> { (byte)(0x80 | op) };
                if (data.Length < 126) head.Add((byte)data.Length);
                else if (data.Length < 65536) { head.Add(126); head.Add((byte)(data.Length >> 8)); head.Add((byte)data.Length); }
                else { head.Add(127); for (int s8 = 56; s8 >= 0; s8 -= 8) head.Add((byte)((long)data.Length >> s8)); }
                st.Write(head.ToArray(), 0, head.Count);
                st.Write(data, 0, data.Length);
            }
        };
        // Sender: state when it changes (and every 3 s as a heartbeat), one camera picture per second in turn.
        new Thread(() =>
        {
            string last = null; var lastBeat = DateTime.MinValue; int cam = 0; var lastPic = DateTime.MinValue;
            try
            {
                while (alive)
                {
                    string sj = stateJson;
                    if (sj != last || (DateTime.Now - lastBeat).TotalSeconds >= 3) { frame(1, Encoding.UTF8.GetBytes(sj)); last = sj; lastBeat = DateTime.Now; }
                    int n = playHandles.Length;
                    if (n > 0 && (DateTime.Now - lastPic).TotalMilliseconds >= 1000)
                    {
                        lastPic = DateTime.Now;
                        cam = (cam + 1) % n;
                        var jpg = Snapshot(cam);
                        if (jpg != null) { var b = new byte[jpg.Length + 1]; b[0] = (byte)cam; Buffer.BlockCopy(jpg, 0, b, 1, jpg.Length); frame(2, b); }
                    }
                    Thread.Sleep(100);
                }
            }
            catch { alive = false; try { st.Close(); } catch { } }
        }) { IsBackground = true }.Start();
        // Receiver: commands from the phone.
        try
        {
            while (alive)
            {
                int b0 = st.ReadByte(), b1 = st.ReadByte();
                if (b0 < 0 || b1 < 0) break;
                int op = b0 & 0x0F; long len = b1 & 0x7F;
                if (len == 126) len = (st.ReadByte() << 8) | st.ReadByte();
                else if (len == 127) { len = 0; for (int k = 0; k < 8; k++) len = (len << 8) | (uint)st.ReadByte(); }
                if (len > 65536) break;
                var mask = new byte[4]; ReadAll(st, mask, 4);
                var data = new byte[len]; ReadAll(st, data, (int)len);
                if ((b1 & 0x80) != 0) for (int k = 0; k < len; k++) data[k] ^= mask[k % 4];
                if (op == 8) break;                                   // close
                if (op == 9) { frame(10, data); continue; }           // ping -> pong
                if (op == 1) Command(Encoding.UTF8.GetString(data));
            }
        }
        catch (IOException) { }
        catch (Exception ex) { Log("ws: " + ex.Message); }
        alive = false;
    }

    static void ReadAll(Stream st, byte[] buf, int n)
    {
        for (int got = 0; got < n; )
        {
            int r = st.Read(buf, got, n - got);
            if (r <= 0) throw new IOException("cerrado");
            got += r;
        }
    }

    // Commands say the wanted result, so a repeated one changes nothing more: "big N" (N=-1: all), "hd N 0|1", "order k,k,..".
    // They run on the screen thread when it is free; the new state goes back to the phone by itself.
    void Command(string cmd)
    {
        var p = cmd.Split(' ');
        int i = -1; if (p.Length > 1) int.TryParse(p[1], out i);
        BeginInvoke((MethodInvoker)delegate
        {
            Tile t = i >= 0 && i < tiles.Count ? tiles[i] : null;
            if (p[0] == "big") { if (big != t) { big = t; DoLayout(); } }
            else if (p[0] == "hd" && t != null && p.Length > 2) SetHd(t, p[2] == "1");
            else if (p[0] == "order" && p.Length > 1) SetOrder(p[1].Split(','));
            // Phone joystick on camera i: "ptz i dx dy" (drag in phone pixels), "ptzstop".
            else if (p[0] == "ptz" && t != null && !CanPtz(t) && p.Length > 3)
            {
                // Joystick on a camera without PTZ: move the zoomed picture (each command = one small step).
                int dx, dy; int.TryParse(p[2], out dx); int.TryParse(p[3], out dy);
                if (t == big && t.Zoom > 1)
                {
                    t.PanX = Math.Max(-1, Math.Min(1, t.PanX + dx / 1500.0 / (t.Zoom - 1)));
                    t.PanY = Math.Max(-1, Math.Min(1, t.PanY + dy / 1500.0 / (t.Zoom - 1)));
                    DoLayout();
                }
            }
            else if (p[0] == "ptz" && t != null && CanPtz(t) && p.Length > 3) { int dx, dy; int.TryParse(p[2], out dx); int.TryParse(p[3], out dy); PtzDrag(t.Ch, dx * 2, dy * 2); }
            else if (p[0] == "ptzstop") PtzStop();
            // Panorama of the split camera: "pano 1" (simple), "pano 2" (advanced), "pano 0" (both lenses again).
            else if (p[0] == "pano" && p.Length > 1) { if (p[1] == "0") PanoOff(); else PanoOn(p[1] == "2" ? 2 : 1); }
            // Phone zoom buttons: "zoom i 1" (in) / "zoom i -1" (out): one digital zoom step each.
            else if (p[0] == "zoom" && t != null && p.Length > 2) ZoomStep(t, p[2] == "1" ? 1 : -1);
            // Test: "ptzraw channel cmd speed" moves a recorder channel directly ("ptzstop" stops it).
            else if (p[0] == "ptzraw" && p.Length > 3) { int ch, pc, sp; int.TryParse(p[1], out ch); int.TryParse(p[2], out pc); int.TryParse(p[3], out sp); PtzStart(ch, (uint)pc, pc >= 0x20 ? sp : 0, sp); }
        });
    }

    void HandleRequest(Stream st, string req)
    {
        var parts = req.Split(' ');
        string path = parts.Length > 1 ? parts[1] : "/";
        string q = path.Contains("?") ? path.Substring(path.IndexOf('?') + 1) : "";
        path = path.Split('?')[0];
        int i = -1, j = -1; string on = null, order = null;
        foreach (var kv in q.Split('&'))
        {
            if (kv.StartsWith("i=")) int.TryParse(kv.Substring(2), out i);
            if (kv.StartsWith("j=")) int.TryParse(kv.Substring(2), out j);
            if (kv.StartsWith("on=")) on = kv.Substring(3);
            if (kv.StartsWith("o=")) order = Uri.UnescapeDataString(kv.Substring(2));
        }

        if (path == "/") { Send(st, "200 OK", "text/html; charset=utf-8", Encoding.UTF8.GetBytes(Page)); return; }
        if (path.StartsWith("/grid"))
        {
            byte[] jpg = null; var vws = viewOf; var hv2 = halves;
            for (int k = 0; k < vws.Length && k < hv2.Length; k++) if (hv2[k] == 3 && vws[k] != null) jpg = vws[k].GridJpeg(480);
            if (jpg == null) Send(st, "404 Not Found", "text/plain", new byte[0]); else Send(st, "200 OK", "image/jpeg", jpg);
            return;
        }
        if (path.StartsWith("/snap")) { var jpg = Snapshot(i); if (jpg == null) Send(st, "404 Not Found", "text/plain", new byte[0]); else Send(st, "200 OK", "image/jpeg", jpg); return; }
        // The phone never waits for the screen: the action runs on the screen thread when it is free
        // (restarting a video can keep it busy 1-2 s). The answer waits at most 300 ms for it, then sends what is known.
        var done = new ManualResetEvent(false);
        BeginInvoke((MethodInvoker)delegate
        {
            Tile t = i >= 0 && i < tiles.Count ? tiles[i] : null;
            // Every action says the wanted result (not "switch"), so a repeated request changes nothing more.
            if (path == "/api/big") { if (big != t) { big = t; DoLayout(); } }          // i=-1: all cameras
            else if (path == "/api/all") { if (big != null) { big = null; DoLayout(); } }
            else if (path == "/api/hd" && t != null && on != null) SetHd(t, on == "1");
            else if (path == "/api/order" && order != null) SetOrder(order.Split(','));
            UpdateState();
            done.Set();
        });
        done.WaitOne(300);
        Send(st, "200 OK", "application/json", Encoding.UTF8.GetBytes(stateJson));
    }

    static void Send(Stream st, string status, string type, byte[] body)
    {
        var head = Encoding.ASCII.GetBytes("HTTP/1.1 " + status + "\r\nContent-Type: " + type + "\r\nContent-Length: " + body.Length + "\r\nCache-Control: no-store\r\nConnection: keep-alive\r\n\r\n");
        st.Write(head, 0, head.Length);
        st.Write(body, 0, body.Length);
    }

    // Current picture of camera i as a small JPEG (only its half for a split channel).
    byte[] Snapshot(int i)
    {
        long play = 0; int half = 0;
        var ph = playHandles; var hv = halves;   // ready copies: no waiting for the screen thread
        if (i >= 0 && i < ph.Length) { play = ph[i]; half = hv[i]; }
        if (play == 0) return null;
        var vws = viewOf;
        // Drawn by me: my last picture. The panorama's card shows it with the bent grid of each lens on top.
        if (i < vws.Length && vws[i] != null) return vws[i].Half == 0 ? (vws[i].GridJpeg(480) ?? vws[i].Jpeg(480)) : vws[i].Jpeg(480);
        lock (snapLock)
        {
            string f = CaptureFile(play, "camaras_snap_" + i);
            if (f == null) return null;
            using (var src = new Bitmap(f))
            {
                int h = half == 0 ? src.Height : src.Height / 2, y = half == 2 ? src.Height / 2 : 0;
                int w = 480, oh = Math.Max(1, h * w / src.Width);
                using (var dst = new Bitmap(w, oh))
                using (var g = Graphics.FromImage(dst))
                using (var ms = new MemoryStream())
                {
                    g.InterpolationMode = System.Drawing.Drawing2D.InterpolationMode.HighQualityBicubic;
                    g.DrawImage(src, new Rectangle(0, 0, w, oh), new Rectangle(0, y, src.Width, h), GraphicsUnit.Pixel);
                    dst.Save(ms, System.Drawing.Imaging.ImageFormat.Jpeg);
                    return ms.ToArray();
                }
            }
        }
    }
    static object snapLock = new object();


    // Saves the current full picture of a stream to a temp file. JPEG first; some SDK builds only write BMP.
    static string CaptureFile(long play, string name)
    {
        string f = Path.Combine(Path.GetTempPath(), name + ".jpg");
        try { File.Delete(f); } catch { }
        if (Native.CLIENT_CapturePictureEx(play, f, 1) && File.Exists(f)) return f;
        string e1 = Native.CLIENT_GetLastError().ToString("X");
        f = Path.ChangeExtension(f, ".bmp");
        try { File.Delete(f); } catch { }
        if (Native.CLIENT_CapturePicture(play, f) && File.Exists(f)) return f;
        Log("foto " + name + ": error 0x" + e1 + " / 0x" + Native.CLIENT_GetLastError().ToString("X"));
        return null;
    }


    // ---- Panorama: find where lens B sits over lens A, from one picture of the channel. ----
    // Both lenses see a strip of the same scene. Search size and place of B that make that strip match best
    // (on edge images, so different brightness does not matter): first coarse on small pictures, then fine.
    // Turns the panorama on in mode 1 or 2, switches mode, or works it out again (same mode pressed again), from a picture
    // taken now: the moving lens may point somewhere else than last time.
    void PanoOn(int mode)
    {
        var split = SplitChannels();
        var todo = new List<Tile>();
        foreach (var t in tiles)
        {
            if ((t.Half != 1 && t.Half != 3) || !split.Contains(t.Ch) || t.Play == 0) continue;
            todo.Add(t);
        }
        if (todo.Count == 0 || panoBusy) return;
        panoBusy = true; panoMsg = "Calculando la panoramica..."; UpdateState();
        foreach (var t0 in todo)
        {
            var t = t0; long play = t.Play;
            // The last advanced result (lens shape and turn) helps the next fit, also after mode 1 was used.
            PanoCal old; pano.TryGetValue(t.Ch, out old); double[] prev = old != null ? old.C2 : null;
            new Thread(() =>
            {
                double[] c1 = null, c2 = null; string err = null;
                var sw = System.Diagnostics.Stopwatch.StartNew();
                try
                {
                    // Right after the squares were built again the video may not have started yet: keep trying for 8 s.
                    string f = null;
                    for (int tries = 0; f == null && tries < 16; tries++) { if (tries > 0) Thread.Sleep(500); lock (snapLock) f = CaptureFile(play, "camaras_pano"); }
                    if (f == null) err = "no pude sacar la foto";
                    else using (var src = new Bitmap(f))
                    {
                        c1 = Calibrate(src, out err);
                        if (c1 != null && mode == 2) c2 = Calibrate2(src, c1, prev, out err);
                    }
                }
                catch (Exception ex) { err = ex.Message; }
                BeginInvoke((MethodInvoker)delegate
                {
                    panoBusy = false;
                    if (c1 == null || (mode == 2 && c2 == null)) { panoMsg = "No pude armar la panoramica: " + err; Log("panoramica canal " + t.Ch + ": " + err); UpdateState(); return; }
                    pano[t.Ch] = new PanoCal { Mode = mode, C1 = c1, C2 = c2 ?? prev }; SavePano(); panoMsg = "";
                    Log("panoramica " + mode + " canal " + t.Ch + " en " + sw.ElapsedMilliseconds + " ms: " + Nums(c1) + (c2 != null ? " | " + Nums(c2) : ""));
                    Rebuild();
                });
            }) { IsBackground = true }.Start();
        }
    }

    static string Nums(double[] a) { var inv = System.Globalization.CultureInfo.InvariantCulture; var l = new List<string>(); foreach (var x in a) l.Add(x.ToString("0.#####", inv)); return string.Join(" ", l.ToArray()); }

    void PanoOff()
    {
        panoMsg = "";
        if (pano.Count == 0) { UpdateState(); return; }
        pano.Clear(); SavePano(); Rebuild();
    }

    void SavePano()
    {
        var lines = new List<string>();
        foreach (var kv in pano) lines.Add(kv.Key + " " + kv.Value.Mode + " " + Nums(kv.Value.C1) + (kv.Value.C2 != null ? " " + Nums(kv.Value.C2) : ""));
        try { File.WriteAllLines(PanoFile, lines.ToArray()); } catch { }
    }

    // Builds all squares again with the same cameras (the order file keeps their places).
    void Rebuild()
    {
        var chans = new List<int>(); foreach (var t in tiles) if (!chans.Contains(t.Ch)) chans.Add(t.Ch);
        chans.Sort();
        BuildTiles(chans);
    }

    // ---- Panorama drawing: one stream, both lenses from the same decoded frame, so they never get out of step. ----
    // The SDK gives each frame as I420 (Y plane, then U and V planes at half width and half height). A lookup table,
    // made once per frame size, says for each output pixel which pixel of lens A and of lens B to take, and how much
    // of each (soft seam in mode 2). Then I draw the result into the panel with GDI, from the SDK's own thread.
    static Dictionary<long, PanoView[]> views = new Dictionary<long, PanoView[]>();
    static Native.RealDataCb panoCb = (h, type, buf, size, param, user) =>
    {
        if (type != 2) { lock (bytesPerPlay) { long b; bytesPerPlay.TryGetValue(h, out b); bytesPerPlay[h] = b + size; } return; }
        PanoView[] vs; lock (views) views.TryGetValue(h, out vs);
        // For decoded frames, param points to {width, height, ...} of the whole frame (both lenses).
        if (vs != null && param != 0)
        {
            int w = Marshal.ReadInt32(new IntPtr(param)), hh = Marshal.ReadInt32(new IntPtr(param), 4);
            foreach (var v in vs) try { v.Frame(buf, w, hh); } catch (Exception ex) { Log("cuadro: " + ex.Message); }
        }
    };

    class PanoView
    {
        [DllImport("user32.dll")] static extern IntPtr GetDC(IntPtr h);
        [DllImport("user32.dll")] static extern int ReleaseDC(IntPtr h, IntPtr dc);
        [DllImport("gdi32.dll")] static extern int SetStretchBltMode(IntPtr dc, int mode);
        [DllImport("gdi32.dll")] static extern bool SetBrushOrgEx(IntPtr dc, int x, int y, IntPtr old);
        [DllImport("gdi32.dll")] static extern int StretchDIBits(IntPtr dc, int x, int y, int w, int h, int sx, int sy, int sw, int sh, IntPtr bits, byte[] bmi, uint usage, uint rop);

        public PanoCal Cal; public IntPtr Target; public volatile int TW, TH;
        public int Half;   // 1 or 2: just that half of the frame (no panorama, Cal is null)
        int fw, fh, OW, OH;
        int[] ia, ib, ua, ub; byte[] wa;   // per output pixel: Y and U/V index in lens A and B (-1 = none), share of A (0-255)
        int[] pix; GCHandle pin; byte[] bmi;

        // Width / height of the picture, from the calibration alone (the layout needs it before the first frame).
        public double Aspect()
        {
            if (Half > 0) return fh > 0 ? (double)fw / (fh / 2) : 16.0 / 9;
            if (Cal.Mode == 2) { var c = Cal.C2; return (c[6] - c[5]) / (c[8] - c[7]); }
            double x0, x1, y0, y1, seam; PanoBox(Cal.C1, out x0, out x1, out y0, out y1, out seam);
            return (x1 - x0) / (y1 - y0);
        }

        // A half is drawn at the size it shows on screen (never bigger than the camera's own pixels): small squares cost little.
        int HalfWidth(int w) { return Math.Max(64, Math.Min(w, TW)) & ~1; }

        void BuildHalf(int w, int h)
        {
            int lw = w, lh = h / 2;
            OW = HalfWidth(w); OH = Math.Max(2, OW * lh / lw);
            int n = OW * OH;
            ia = new int[n]; ib = new int[n]; ua = new int[n]; ub = new int[n]; wa = new byte[n];
            for (int oy = 0; oy < OH; oy++)
            {
                int py = (int)((oy + 0.5) * lh / OH) + (Half == 2 ? lh : 0);
                for (int ox = 0; ox < OW; ox++)
                {
                    int px = (int)((ox + 0.5) * lw / OW), k = oy * OW + ox;
                    ia[k] = py * w + px; ua[k] = (py / 2) * (w / 2) + px / 2; ib[k] = -1; wa[k] = 255;
                }
            }
            if (pin.IsAllocated) pin.Free();
            pix = new int[n]; pin = GCHandle.Alloc(pix, GCHandleType.Pinned);
            bmi = new byte[40];
            BitConverter.GetBytes(40).CopyTo(bmi, 0); BitConverter.GetBytes(OW).CopyTo(bmi, 4); BitConverter.GetBytes(-OH).CopyTo(bmi, 8);
            BitConverter.GetBytes((short)1).CopyTo(bmi, 12); BitConverter.GetBytes((short)32).CopyTo(bmi, 14);
        }

        void Build(int w, int h)
        {
            if (Half > 0) { BuildHalf(w, h); return; }
            int lw = w, lh = h / 2;
            double x0 = 0, x1 = 1, y0 = 0, y1 = 1, seam, s = 1, dx = 0, dy = 0;
            Lens lens = null;
            if (Cal.Mode == 2)
            {
                var c = Cal.C2; lens = new Lens(c, lw, lh);
                OW = Math.Min(1920, (int)(lens.F * (c[6] - c[5])));
                OH = (int)(OW * (c[8] - c[7]) / (c[6] - c[5]));
            }
            else
            {
                PanoBox(Cal.C1, out x0, out x1, out y0, out y1, out seam);
                s = Cal.C1[0]; dx = Cal.C1[1]; dy = Cal.C1[2];
                OW = Math.Min(1920, (int)(lw * (x1 - x0)));
                OH = (int)(OW * (y1 - y0) / (x1 - x0));
            }
            OW = Math.Max(2, OW & ~1); OH = Math.Max(2, OH);
            int n = OW * OH;
            ia = new int[n]; ib = new int[n]; ua = new int[n]; ub = new int[n]; wa = new byte[n];
            var ax = new double[OW]; var ay = new double[OW]; var bx = new double[OW]; var by = new double[OW];
            var inA = new bool[OW]; var inB = new bool[OW];
            var ray = new double[3]; var rb = new double[3]; var q = new double[2];
            // Half width of the soft seam, in output pixels: narrow when the seam follows the best path.
            double band = Cal.Mode == 2 ? (SeamAt(Cal.C2, 0.5) >= 0 ? Math.Max(2, OW / 200.0) : Math.Max(4, OW / 60.0)) : 0;
            for (int oy = 0; oy < OH; oy++)
            {
                int minA = int.MaxValue, maxA = -1, minB = int.MaxValue, maxB = -1;
                for (int ox = 0; ox < OW; ox++)
                {
                    if (Cal.Mode == 2)
                    {
                        var c = Cal.C2;
                        double ang = c[5] + (ox + 0.5) / OW * (c[6] - c[5]), hgt = c[7] + (oy + 0.5) / OH * (c[8] - c[7]);
                        // Fine bending (see Field): lens B is taken shifted by the bending, or, in newer calibrations,
                        // lens A by minus half of it and lens B by plus half, so each lens bends only half as much.
                        double du, dv; FieldAt(c, (ox + 0.5) / OW, (oy + 0.5) / OH, out du, out dv);
                        double fa = FieldBoth(c) ? -0.5 : 0, fb = FieldBoth(c) ? 0.5 : 1;
                        double angA = ang + fa * du * (c[6] - c[5]), hgtA = hgt + fa * dv * (c[8] - c[7]);
                        ray[0] = Math.Sin(angA); ray[1] = hgtA; ray[2] = Math.Cos(angA);
                        if (lens.Project(ray, q)) { ax[ox] = q[0]; ay[ox] = q[1]; } else ax[ox] = -1e9;
                        double angB = ang + fb * du * (c[6] - c[5]), hgtB = hgt + fb * dv * (c[8] - c[7]);
                        ray[0] = Math.Sin(angB); ray[1] = hgtB; ray[2] = Math.Cos(angB);
                        lens.ToB(ray, rb);
                        if (lens.Project(rb, q)) { bx[ox] = q[0]; by[ox] = q[1]; } else bx[ox] = -1e9;
                    }
                    else
                    {
                        double X = x0 + (ox + 0.5) / OW * (x1 - x0), Y = y0 + (oy + 0.5) / OH * (y1 - y0);
                        ax[ox] = X * lw; ay[ox] = Y * lw; bx[ox] = (X - dx) / s * lw; by[ox] = (Y - dy) / s * lw;
                    }
                    inA[ox] = ax[ox] >= 0 && ax[ox] <= lw - 1 && ay[ox] >= 0 && ay[ox] <= lh - 1;
                    inB[ox] = bx[ox] >= 0 && bx[ox] <= lw - 1 && by[ox] >= 0 && by[ox] <= lh - 1;
                    if (inA[ox]) { minA = Math.Min(minA, ox); maxA = ox; }
                    if (inB[ox]) { minB = Math.Min(minB, ox); maxB = ox; }
                }
                // Seam in the middle of the columns both lenses have in this row.
                bool bRight = (minB + maxB) > (minA + maxA);
                double lo = Math.Max(minA, minB), hi = Math.Min(maxA, maxB);
                double seamX = lo <= hi ? (lo + hi) / 2 : (bRight ? maxA : minA);
                if (Cal.Mode == 2) { double su = SeamAt(Cal.C2, (oy + 0.5) / OH); if (su >= 0) seamX = su * OW - 0.5; }
                for (int ox = 0; ox < OW; ox++)
                {
                    int k = oy * OW + ox;
                    double share = !inB[ox] ? 1 : !inA[ox] ? 0
                        : band <= 0 ? ((bRight ? ox < seamX : ox >= seamX) ? 1 : 0)
                        : Math.Max(0, Math.Min(1, 0.5 + (bRight ? seamX - ox : ox - seamX) / (2 * band)));
                    wa[k] = (byte)Math.Round(share * 255);
                    if (inA[ox]) { int px = (int)(ax[ox] + 0.5), py = (int)(ay[ox] + 0.5); ia[k] = py * w + px; ua[k] = (py / 2) * (w / 2) + px / 2; } else ia[k] = -1;
                    if (inB[ox]) { int px = (int)(bx[ox] + 0.5), py = (int)(by[ox] + 0.5) + lh; ib[k] = py * w + px; ub[k] = (py / 2) * (w / 2) + px / 2; } else ib[k] = -1;
                }
            }
            if (pin.IsAllocated) pin.Free();
            pix = new int[n]; pin = GCHandle.Alloc(pix, GCHandleType.Pinned);
            bmi = new byte[40];
            BitConverter.GetBytes(40).CopyTo(bmi, 0); BitConverter.GetBytes(OW).CopyTo(bmi, 4); BitConverter.GetBytes(-OH).CopyTo(bmi, 8);
            BitConverter.GetBytes((short)1).CopyTo(bmi, 12); BitConverter.GetBytes((short)32).CopyTo(bmi, 14);
            Log("panoramica: tabla " + OW + "x" + OH + " para cuadros de " + w + "x" + h);
        }

        // YUV to RGB with whole numbers (BT.601, video range).
        static int[] tY = new int[256], tRV = new int[256], tGU = new int[256], tGV = new int[256], tBU = new int[256];
        static byte[] clamp = new byte[1024];
        static PanoView()
        {
            for (int i = 0; i < 256; i++) { tY[i] = 298 * (i - 16); tRV[i] = 409 * (i - 128); tGU[i] = -100 * (i - 128); tGV[i] = -208 * (i - 128); tBU[i] = 516 * (i - 128); }
            for (int i = 0; i < 1024; i++) clamp[i] = (byte)Math.Max(0, Math.Min(255, i - 384));
        }

        public unsafe void Frame(IntPtr buf, int w, int h)
        {
            if (w <= 0 || h <= 0 || !Monitor.TryEnter(this)) return;   // still busy with the last frame: skip this one
            try
            {
                if (w != fw || h != fh || (Half > 0 && HalfWidth(w) != OW)) { Build(w, h); fw = w; fh = h; }
                byte* Y = (byte*)buf, U = Y + w * h, V = U + w * h / 4;
                int n = OW * OH;
                fixed (int* o = pix) fixed (int* pa = ia) fixed (int* pb = ib) fixed (int* qa = ua) fixed (int* qb = ub) fixed (byte* sh = wa) fixed (int* cy = tY) fixed (int* crv = tRV) fixed (int* cgu = tGU) fixed (int* cgv = tGV) fixed (int* cbu = tBU) fixed (byte* cl = clamp)
                {
                    for (int k = 0; k < n; k++)
                    {
                        int a = pa[k], b = pb[k], s = sh[k];
                        int r = 0, g = 0, bl = 0;
                        if (a >= 0 && s > 0)
                        {
                            int yy = cy[Y[a]], u = U[qa[k]], v = V[qa[k]];
                            r = cl[((yy + crv[v] + 128) >> 8) + 384]; g = cl[((yy + cgu[u] + cgv[v] + 128) >> 8) + 384]; bl = cl[((yy + cbu[u] + 128) >> 8) + 384];
                        }
                        if (b >= 0 && s < 255)
                        {
                            int yy = cy[Y[b]], u = U[qb[k]], v = V[qb[k]];
                            int r2 = cl[((yy + crv[v] + 128) >> 8) + 384], g2 = cl[((yy + cgu[u] + cgv[v] + 128) >> 8) + 384], b2 = cl[((yy + cbu[u] + 128) >> 8) + 384];
                            if (s == 0) { r = r2; g = g2; bl = b2; }
                            else { r = (r * s + r2 * (255 - s)) / 255; g = (g * s + g2 * (255 - s)) / 255; bl = (bl * s + b2 * (255 - s)) / 255; }
                        }
                        o[k] = (r << 16) | (g << 8) | bl;
                    }
                }
                int tw = TW, th = TH;
                if (tw > 0 && th > 0)
                {
                    IntPtr dc = GetDC(Target);
                    if (dc != IntPtr.Zero)
                    {
                        // Smooth scaling only when the picture is shrunk a lot (small squares); it costs time.
                        if (tw < OW * 0.7) { SetStretchBltMode(dc, 4); SetBrushOrgEx(dc, 0, 0, IntPtr.Zero); } else SetStretchBltMode(dc, 3);
                        StretchDIBits(dc, 0, 0, tw, th, 0, 0, OW, OH, pin.AddrOfPinnedObject(), bmi, 0, 0x00CC0020);
                        ReleaseDC(Target, dc);
                    }
                }
            }
            finally { Monitor.Exit(this); }
        }

        // Curiosity for the phone: the last panorama, darker, with a straight grid of each lens drawn the way
        // the panorama bends it (light blue: camera 3 = lens A, orange: camera 4 = lens B).
        public byte[] GridJpeg(int w)
        {
            lock (this)
            {
                if (pix == null || Half > 0 || Cal == null || fw == 0) return null;
                int lw = fw, lh = fh / 2;
                double x0 = 0, x1 = 1, y0 = 0, y1 = 1, seam; Lens lens = null;
                if (Cal.Mode == 2) lens = new Lens(Cal.C2, lw, lh); else PanoBox(Cal.C1, out x0, out x1, out y0, out y1, out seam);
                var r = new double[3]; var ra = new double[3];
                // Lens pixel -> place in the picture (0..1), or false when it falls off.
                Func<int, double, double, PointF?> map = (lensNo, x, y) =>
                {
                    double u, v;
                    if (lens != null)
                    {
                        var c = Cal.C2;
                        lens.Unproject(x, y, r); if (lensNo == 1) { lens.ToA(r, ra); Array.Copy(ra, r, 3); }
                        double ang = Math.Atan2(r[0], r[2]), hgt = r[1] / Math.Sqrt(r[0] * r[0] + r[2] * r[2]);
                        u = (ang - c[5]) / (c[6] - c[5]); v = (hgt - c[7]) / (c[8] - c[7]);
                        double du, dv; FieldAt(c, u, v, out du, out dv);
                        double f = FieldBoth(c) ? (lensNo == 1 ? 0.5 : -0.5) : (lensNo == 1 ? 1 : 0);
                        u -= f * du; v -= f * dv;
                    }
                    else
                    {
                        double X = x / lw, Y = y / lw;
                        if (lensNo == 1) { X = Cal.C1[1] + Cal.C1[0] * X; Y = Cal.C1[2] + Cal.C1[0] * Y; }
                        u = (X - x0) / (x1 - x0); v = (Y - y0) / (y1 - y0);
                    }
                    if (u < -0.3 || u > 1.3 || v < -0.5 || v > 1.5) return null;
                    return new PointF((float)u, (float)v);
                };
                int oh = Math.Max(1, OH * w / OW);
                using (var src = new Bitmap(OW, OH, OW * 4, System.Drawing.Imaging.PixelFormat.Format32bppRgb, pin.AddrOfPinnedObject()))
                using (var dst = new Bitmap(w, oh))
                using (var g = Graphics.FromImage(dst))
                using (var ms = new MemoryStream())
                {
                    g.InterpolationMode = System.Drawing.Drawing2D.InterpolationMode.HighQualityBilinear;
                    g.DrawImage(src, new Rectangle(0, 0, w, oh));
                    using (var dim = new SolidBrush(Color.FromArgb(150, 0, 0, 0))) g.FillRectangle(dim, 0, 0, w, oh);
                    g.SmoothingMode = System.Drawing.Drawing2D.SmoothingMode.AntiAlias;
                    for (int lensNo = 0; lensNo < 2; lensNo++)
                        using (var pen = new Pen(lensNo == 0 ? Color.FromArgb(220, 80, 200, 255) : Color.FromArgb(220, 255, 150, 40), 1.5f))
                            for (int dir = 0; dir < 2; dir++)
                                for (int i = 0; i <= 12; i++)
                                {
                                    PointF? last = null;
                                    for (int j = 0; j <= 48; j++)
                                    {
                                        double a = (double)i / 12, b = (double)j / 48;
                                        double x = (dir == 0 ? a : b) * (lw - 1), y = (dir == 0 ? b : a) * (lh - 1);
                                        var pt = map(lensNo, x, y);
                                        if (pt != null) { var pp = new PointF(pt.Value.X * w, pt.Value.Y * oh); if (last != null) g.DrawLine(pen, last.Value, pp); last = pp; }
                                        else last = null;
                                    }
                                }
                    dst.Save(ms, System.Drawing.Imaging.ImageFormat.Jpeg);
                    return ms.ToArray();
                }
            }
        }

        // Last pasted frame as a JPEG for the phone.
        public byte[] Jpeg(int w)
        {
            lock (this)
            {
                if (pix == null) return null;
                using (var src = new Bitmap(OW, OH, OW * 4, System.Drawing.Imaging.PixelFormat.Format32bppRgb, pin.AddrOfPinnedObject()))
                using (var dst = new Bitmap(w, Math.Max(1, OH * w / OW)))
                using (var g = Graphics.FromImage(dst))
                using (var ms = new MemoryStream())
                {
                    g.InterpolationMode = System.Drawing.Drawing2D.InterpolationMode.HighQualityBilinear;
                    g.DrawImage(src, new Rectangle(0, 0, dst.Width, dst.Height));
                    dst.Save(ms, System.Drawing.Imaging.ImageFormat.Jpeg);
                    return ms.ToArray();
                }
            }
        }
    }

    // ---- Lens model for the advanced panorama. ----
    // Both lenses are the same kind: a ray at angle t from the lens axis lands at radius F*(t + K*t^3) from the
    // middle of the picture (K = 0: fisheye-like, K = 1/3: almost a normal lens). Lens B is lens A turned by yaw,
    // pitch and roll (the lenses are a few cm apart; that small shift is ignored, so very close things can show twice).
    class Lens
    {
        public double F, K, Cx, Cy; public double[] R;   // R turns a ray seen by A into the same ray seen by B
        public Lens(double[] c, int lw, int lh) : this(c[0] * lw, c[1], c[2], c[3], c[4], lw, lh) { }
        public Lens(double f, double k, double yaw, double pitch, double roll, int lw, int lh)
        {
            F = f; K = k; Cx = lw / 2.0; Cy = lh / 2.0;
            double cy = Math.Cos(yaw), sy = Math.Sin(yaw), cp = Math.Cos(pitch), sp = Math.Sin(pitch), cr = Math.Cos(roll), sr = Math.Sin(roll);
            var Ry = new[] { cy, 0, sy, 0, 1, 0, -sy, 0, cy }; var Rx = new[] { 1, 0, 0, 0, cp, -sp, 0, sp, cp }; var Rz = new[] { cr, -sr, 0, sr, cr, 0, 0, 0, 1 };
            R = Mul(Rz, Mul(Rx, Ry));
        }
        static double[] Mul(double[] a, double[] b)
        {
            var r = new double[9];
            for (int i = 0; i < 3; i++) for (int j = 0; j < 3; j++) r[i * 3 + j] = a[i * 3] * b[j] + a[i * 3 + 1] * b[3 + j] + a[i * 3 + 2] * b[6 + j];
            return r;
        }
        public void ToB(double[] v, double[] o) { o[0] = R[0] * v[0] + R[1] * v[1] + R[2] * v[2]; o[1] = R[3] * v[0] + R[4] * v[1] + R[5] * v[2]; o[2] = R[6] * v[0] + R[7] * v[1] + R[8] * v[2]; }
        public void ToA(double[] v, double[] o) { o[0] = R[0] * v[0] + R[3] * v[1] + R[6] * v[2]; o[1] = R[1] * v[0] + R[4] * v[1] + R[7] * v[2]; o[2] = R[2] * v[0] + R[5] * v[1] + R[8] * v[2]; }
        // Ray (any length) -> pixel. False for rays behind the lens.
        public bool Project(double[] r, double[] q)
        {
            double len = Math.Sqrt(r[0] * r[0] + r[1] * r[1] + r[2] * r[2]); if (r[2] < 0.05 * len) return false;
            double t = Math.Acos(Math.Min(1, r[2] / len)), xy = Math.Sqrt(r[0] * r[0] + r[1] * r[1]);
            if (xy < 1e-12) { q[0] = Cx; q[1] = Cy; return true; }
            double rd = F * (t + K * t * t * t);
            q[0] = Cx + r[0] / xy * rd; q[1] = Cy + r[1] / xy * rd; return true;
        }
        // Pixel -> ray of length 1 (Newton steps to undo the lens bending).
        public void Unproject(double px, double py, double[] r)
        {
            double u = px - Cx, v = py - Cy, rd = Math.Sqrt(u * u + v * v) / F;
            if (rd < 1e-9) { r[0] = 0; r[1] = 0; r[2] = 1; return; }
            double t = rd;
            for (int i = 0; i < 8; i++) { double g = t + K * t * t * t - rd, d = 1 + 3 * K * t * t; t -= g / d; }
            double sn = Math.Sin(t) / (rd * F); r[0] = u * sn; r[1] = v * sn; r[2] = Math.Cos(t);
        }
    }

    // Gray values of part of a bitmap, full size.
    static double[] Lum(Bitmap src, Rectangle part)
    {
        int w = part.Width, h = part.Height; var l = new double[w * h];
        using (var b = src.Clone(part, System.Drawing.Imaging.PixelFormat.Format24bppRgb))
        {
            var d = b.LockBits(new Rectangle(0, 0, w, h), System.Drawing.Imaging.ImageLockMode.ReadOnly, b.PixelFormat);
            var raw = new byte[d.Stride * h]; Marshal.Copy(d.Scan0, raw, 0, raw.Length); b.UnlockBits(d);
            for (int y = 0; y < h; y++) for (int x = 0; x < w; x++) { int i = y * d.Stride + x * 3; l[y * w + x] = 0.114 * raw[i] + 0.587 * raw[i + 1] + 0.299 * raw[i + 2]; }
        }
        return l;
    }

    // Advanced panorama: lens shape and turn of lens B, from points both lenses see.
    //  1. Corners in B (Harris), each searched in A near where mode 1 puts it (patch correlation).
    //  2. Fit F, K, yaw, pitch, roll so B's points land on A's (Nelder-Mead, several starts, robust error).
    //  3. Cylinder: angles from A's left edge to B's right edge (or the other way), heights where both are complete.
    // Returns {F/width, K, yaw, pitch, roll, a0, a1, h0, h1}, or null with the reason.
    static double[] Calibrate2(Bitmap src, double[] c1, double[] prev, out string err)
    {
        err = null;
        int W = src.Width, H = src.Height / 2;
        var A = Lum(src, new Rectangle(0, 0, W, H)); var B = Lum(src, new Rectangle(0, H, W, H));
        // 1. corners of B
        var resp = new double[W * H];
        for (int y = 2; y < H - 2; y++) for (int x = 2; x < W - 2; x++)
        {
            double sxx = 0, syy = 0, sxy = 0;
            for (int j = -1; j <= 1; j++) for (int i = -1; i <= 1; i++) { int k = (y + j) * W + x + i; double gx = B[k + 1] - B[k - 1], gy = B[k + W] - B[k - W]; sxx += gx * gx; syy += gy * gy; sxy += gx * gy; }
            resp[y * W + x] = sxx * syy - sxy * sxy - 0.05 * (sxx + syy) * (sxx + syy);
        }
        var cs = new List<double[]>();
        for (int y = Math.Max(6, (int)(H * 0.05)); y < H - Math.Max(6, (int)(H * 0.1)); y++) for (int x = 10; x < W - 10; x++)
        {
            double v = resp[y * W + x]; if (v <= 0) continue; bool top = true;
            for (int j = -4; j <= 4 && top; j++) for (int i = -4; i <= 4; i++) if (resp[(y + j) * W + x + i] > v) { top = false; break; }
            if (top) cs.Add(new double[] { x, y, v });
        }
        cs.Sort((a, b) => b[2].CompareTo(a[2]));
        if (cs.Count > 600) cs.RemoveRange(600, cs.Count - 600);
        // patch correlation
        int P = 7, Rs = 40 * W / 640;
        Func<int, int, int, int, double> ncc = (axp, ayp, bxp, byp) =>
        {
            double sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0; int n = 0;
            for (int j = -P; j <= P; j++) for (int i = -P; i <= P; i++) { double a = A[(ayp + j) * W + axp + i], b = B[(byp + j) * W + bxp + i]; sa += a; sb += b; saa += a * a; sbb += b * b; sab += a * b; n++; }
            double va = saa / n - (sa / n) * (sa / n), vb = sbb / n - (sb / n) * (sb / n);
            return va > 4 && vb > 4 ? (sab / n - sa / n * sb / n) / Math.Sqrt(va * vb) : -1;
        };
        double s1 = c1[0], dx1 = c1[1] * W, dy1 = c1[2] * W;
        var ms = new List<double[]>();
        foreach (var c in cs)
        {
            int bxp = (int)c[0], byp = (int)c[1];
            int px = (int)Math.Round(dx1 + s1 * bxp), py = (int)Math.Round(dy1 + s1 * byp);
            if (px < 2 * P || px >= W - P - 1 || py < P + 1 || py >= H - P - 1) continue;
            double best = -2, second = -2; int bax = 0, bay = 0;
            for (int y = Math.Max(P + 1, py - Rs); y <= Math.Min(H - P - 2, py + Rs); y++)
                for (int x = Math.Max(P + 1, px - Rs); x <= Math.Min(W - P - 2, px + Rs); x++)
                {
                    double v = ncc(x, y, bxp, byp);
                    bool far = Math.Abs(x - bax) + Math.Abs(y - bay) > 4;
                    if (v > best) { if (far) second = best; best = v; bax = x; bay = y; }
                    else if (v > second && far) second = v;
                }
            if (best > 0.85 && best - second > 0.05) ms.Add(new double[] { bax, bay, bxp, byp });
        }
        Log("panoramica 2: " + cs.Count + " esquinas, " + ms.Count + " puntos en comun");
        if (ms.Count < 12) { err = "muy pocos puntos en comun entre las dos lentes (" + ms.Count + ")"; return null; }
        // 2. fit
        var ra = new double[3]; var rb = new double[3]; var q = new double[2];
        Func<double[], double> cost = pr =>
        {
            if (pr[0] < 0.15 * W || pr[0] > 3 * W || pr[1] < -0.4 || pr[1] > 0.5) return 1e9;
            var L = new Lens(pr[0], pr[1], pr[2], pr[3], pr[4], W, H);
            var ds = new double[ms.Count];
            for (int i = 0; i < ms.Count; i++)
            {
                var m = ms[i]; L.Unproject(m[2], m[3], rb); L.ToA(rb, ra);
                ds[i] = L.Project(ra, q) ? Math.Sqrt((q[0] - m[0]) * (q[0] - m[0]) + (q[1] - m[1]) * (q[1] - m[1])) : 1000;
            }
            // Only the best 70% count: points on near things (a ladder close to the camera) cannot fit, because the
            // lenses are a few cm apart. The fine bending takes care of them afterwards.
            Array.Sort(ds); int use = Math.Max(8, ds.Length * 7 / 10); double e = 0;
            for (int i = 0; i < use; i++) e += ds[i] < 3 ? ds[i] * ds[i] : 6 * ds[i] - 9;
            return e / use;
        };
        double[] bestP = null; double bestV = double.MaxValue;
        foreach (var f0 in new[] { 250.0, 350, 500 })
            foreach (var k0 in new[] { 0.0, 0.15, 0.3 })
                foreach (var sign in new[] { -1.0, 1.0 })
                {
                    double f = f0 * W / 640, yaw0 = sign * Math.Abs(Math.Atan2(dx1 + s1 * W / 2 - W / 2.0, f));
                    double v; var pr = NelderMead(cost, new[] { f, k0, yaw0, 0, 0 }, new[] { 40.0 * W / 640, 0.05, 0.1, 0.03, 0.03 }, 1500, out v);
                    if (v < bestV) { bestV = v; bestP = pr; }
                }
        if (prev != null && prev.Length >= 5)
        {
            // Start from the last result; also try with its lens shape kept as it was (only the turn is fitted).
            double v; var pr = NelderMead(cost, new[] { prev[0] * W, prev[1], prev[2], prev[3], prev[4] }, new[] { 20.0 * W / 640, 0.03, 0.05, 0.02, 0.02 }, 1500, out v);
            if (v < bestV) { bestV = v; bestP = pr; }
            double fk = prev[0] * W, kk = prev[1];
            Func<double[], double> turn = r => cost(new[] { fk, kk, r[0], r[1], r[2] });
            var pt = NelderMead(turn, new[] { prev[2], prev[3], prev[4] }, new[] { 0.05, 0.02, 0.02 }, 1000, out v);
            // With few shared points (something close covers the strip) the lens shape cannot be trusted from this
            // picture: keep the known shape unless it clearly does not fit.
            if (v < bestV * (ms.Count < 40 ? 3 : 1.1)) { bestV = Math.Min(v, bestV); bestP = new[] { fk, kk, pt[0], pt[1], pt[2] }; }
        }
        var lens = new Lens(bestP[0], bestP[1], bestP[2], bestP[3], bestP[4], W, H);
        var errs = new List<double>();
        foreach (var m in ms) { lens.Unproject(m[2], m[3], rb); lens.ToA(rb, ra); if (lens.Project(ra, q)) errs.Add(Math.Sqrt((q[0] - m[0]) * (q[0] - m[0]) + (q[1] - m[1]) * (q[1] - m[1]))); }
        errs.Sort(); double med = errs.Count > 0 ? errs[errs.Count * 35 / 100] : 99;   // middle of the best 70%
        Log("panoramica 2: ajuste " + Nums(bestP) + ", error medio " + med.ToString("0.0") + " px");
        if (med > 6 * W / 640.0) { err = "no pude ajustar la forma de las lentes (error " + med.ToString("0.0") + " px)"; return null; }
        // 3. cylinder range: angles of the left and right edges of both pictures (middle row)
        var angs = new List<double>();
        foreach (var x in new[] { 0.0, W - 1.0 })
        {
            lens.Unproject(x, H / 2.0, ra); angs.Add(Math.Atan2(ra[0], ra[2]));
            lens.Unproject(x, H / 2.0, rb); lens.ToA(rb, ra); angs.Add(Math.Atan2(ra[0], ra[2]));
        }
        angs.Sort();
        double a0 = angs[0], a1 = angs[3], pad = (a1 - a0) * 0.01; a0 += pad; a1 -= pad;
        // heights: on a small grid, keep the rows where nearly every column has picture
        int GW = 300, GH = 240; double hs = H / 2.0 / lens.F * 1.6;
        var good = new bool[GH]; var ray = new double[3];
        for (int gy = 0; gy < GH; gy++)
        {
            double hgt = -hs + 2 * hs * gy / (GH - 1); int ok = 0;
            for (int gx = 0; gx < GW; gx++)
            {
                double ang = a0 + (a1 - a0) * gx / (GW - 1);
                ray[0] = Math.Sin(ang); ray[1] = hgt; ray[2] = Math.Cos(ang);
                bool inA = lens.Project(ray, q) && q[0] >= 0 && q[0] <= W - 1 && q[1] >= 0 && q[1] <= H - 1;
                lens.ToB(ray, rb);
                bool inB = lens.Project(rb, q) && q[0] >= 0 && q[0] <= W - 1 && q[1] >= 0 && q[1] <= H - 1;
                if (inA || inB) ok++;
            }
            good[gy] = ok >= GW * 0.97;
        }
        int bs = -1, bl = 0;
        for (int gy = 0; gy < GH; ) { if (!good[gy]) { gy++; continue; } int e = gy; while (e < GH && good[e]) e++; if (e - gy > bl) { bl = e - gy; bs = gy; } gy = e; }
        if (bl < 10) { err = "la panoramica queda demasiado finita"; return null; }
        double h0 = -hs + 2 * hs * bs / (GH - 1), h1 = -hs + 2 * hs * (bs + bl - 1) / (GH - 1);
        var basic = new[] { bestP[0] / W, bestP[1], bestP[2], bestP[3], bestP[4], a0, a1, h0, h1 };
        var field = Field(A, B, W, H, lens, a0, a1, h0, h1);
        var all = new double[basic.Length + field.Length]; basic.CopyTo(all, 0); field.CopyTo(all, basic.Length);
        return all;
    }

    // Fine bending for the advanced panorama. The lenses are a few cm apart, so near things (the table edge) land in
    // slightly different places in each lens; the lens model above cannot fix that. So: show both lenses on the
    // cylinder, measure the leftover shift of lens B at many points of the shared strip (patch search), clean the
    // measures, and fill the rest of the picture smoothly so the shift fades to zero over each lens's own part.
    // Each lens then takes half of the shift (A minus half, B plus half), so neither bends much.
    // Returns {NX, NY, gap across, gap down, du[NX*NY], dv[NX*NY]}: a grid over the picture; gaps and shifts as parts
    // of the picture's width and height.
    static double[] Field(double[] A, double[] B, int W, int H, Lens lens, double a0, double a1, double h0, double h1)
    {
        int ow = (int)(lens.F * (a1 - a0)), oh = (int)(ow * (h1 - h0) / (a1 - a0));
        var ca = new double[ow * oh]; var cb = new double[ow * oh]; var va = new bool[ow * oh]; var vb = new bool[ow * oh];
        var ray = new double[3]; var rb = new double[3]; var q = new double[2];
        for (int y = 0; y < oh; y++) for (int x = 0; x < ow; x++)
        {
            double ang = a0 + (x + 0.5) / ow * (a1 - a0), hgt = h0 + (y + 0.5) / oh * (h1 - h0);
            ray[0] = Math.Sin(ang); ray[1] = hgt; ray[2] = Math.Cos(ang);
            int k = y * ow + x;
            if (lens.Project(ray, q) && q[0] >= 0 && q[0] < W - 1 && q[1] >= 0 && q[1] < H - 1) { ca[k] = Bilinear(A, W, q[0], q[1]); va[k] = true; }
            lens.ToB(ray, rb);
            if (lens.Project(rb, q) && q[0] >= 0 && q[0] < W - 1 && q[1] >= 0 && q[1] < H - 1) { cb[k] = Bilinear(B, W, q[0], q[1]); vb[k] = true; }
        }
        int step = Math.Max(8, ow / 90), P = 8, S = Math.Max(16, ow / 30);
        int NX = (ow - 1) / step + 1, NY = (oh - 1) / step + 1, N = NX * NY;
        var du = new double[N]; var dv = new double[N]; var known = new bool[N]; var zero = new bool[N];
        Func<bool[], int, int, bool> ok = (m, x, y) => x >= 0 && y >= 0 && x < ow && y < oh && m[y * ow + x];
        int measured = 0; double maxShift = 0;
        for (int gy = 0; gy < NY; gy++) for (int gx = 0; gx < NX; gx++)
        {
            int px = gx * step, py = gy * step;
            if (!ok(va, px - P, py - P) || !ok(va, px + P, py + P) || !ok(va, px - P, py + P) || !ok(va, px + P, py - P) || !ok(vb, px, py)) continue;
            double sa = 0, saa = 0; int n = 0;
            for (int j = -P; j <= P; j++) for (int i = -P; i <= P; i++) { double a = ca[(py + j) * ow + px + i]; sa += a; saa += a * a; n++; }
            double ma = sa / n, vA = saa / n - ma * ma;
            if (vA < 40) continue;   // plain wall: nothing to measure
            int side = 2 * S + 1; var vals = new double[side * side]; double best = -2;
            for (int dy = -S; dy <= S; dy++) for (int dx = -S; dx <= S; dx++)
            {
                int k = (dy + S) * side + dx + S; vals[k] = -2;
                int qx = px + dx, qy = py + dy;
                if (!ok(vb, qx - P, qy - P) || !ok(vb, qx + P, qy + P) || !ok(vb, qx - P, qy + P) || !ok(vb, qx + P, qy - P)) continue;
                double sb = 0, sbb = 0, sab = 0;
                for (int j = -P; j <= P; j += 2) for (int i = -P; i <= P; i += 2)
                {
                    double a = ca[(py + j) * ow + px + i] - ma, b = cb[(qy + j) * ow + qx + i];
                    sb += b; sbb += b * b; sab += a * b;
                }
                int m2 = (P + 1) * (P + 1);   // every second pixel
                double vB = sbb / m2 - (sb / m2) * (sb / m2);
                vals[k] = vB > 1 ? (sab / m2) / Math.Sqrt(vA * vB) : -1;
                if (vals[k] > best) best = vals[k];
            }
            if (best < 0.75) continue;
            // Along a straight edge (the table) many shifts fit about as well: take the smallest of those.
            int bx = 0, by = 0; double bd = double.MaxValue;
            for (int dy = -S; dy <= S; dy++) for (int dx = -S; dx <= S; dx++)
                if (vals[(dy + S) * side + dx + S] >= best - 0.02 && dx * dx + dy * dy < bd) { bd = dx * dx + dy * dy; bx = dx; by = dy; }
            if (Math.Abs(bx) == S || Math.Abs(by) == S) continue;
            int g = gy * NX + gx; known[g] = true; du[g] = bx; dv[g] = by; measured++;
        }
        // Clean: a measure far from the middle value of its neighbours is wrong (moving thing, repeated pattern).
        var keep = new bool[N];
        for (int gy = 0; gy < NY; gy++) for (int gx = 0; gx < NX; gx++)
        {
            int g = gy * NX + gx; if (!known[g]) continue;
            var lx = new List<double>(); var ly = new List<double>();
            for (int j = -2; j <= 2; j++) for (int i = -2; i <= 2; i++)
            {
                int x = gx + i, y = gy + j; if (x < 0 || y < 0 || x >= NX || y >= NY || !known[y * NX + x]) continue;
                lx.Add(du[y * NX + x]); ly.Add(dv[y * NX + x]);
            }
            lx.Sort(); ly.Sort();
            double mx = lx[lx.Count / 2], my = ly[ly.Count / 2];
            if (lx.Count >= 3 && Math.Abs(du[g] - mx) <= 4 && Math.Abs(dv[g] - my) <= 4) { keep[g] = true; maxShift = Math.Max(maxShift, Math.Max(Math.Abs(mx), Math.Abs(my))); }
        }
        // Where only lens B shows, far enough from the shared strip, the bending is zero.
        for (int gy = 0; gy < NY; gy++)
        {
            int lo = int.MaxValue, hi = -1;
            for (int gx = 0; gx < NX; gx++) { int k = Math.Min(gy * step, oh - 1) * ow + Math.Min(gx * step, ow - 1); if (va[k] && vb[k]) { lo = Math.Min(lo, gx); hi = gx; } }
            int fade = Math.Max(3, NX / 5);
            for (int gx = 0; gx < NX; gx++)
            {
                int k = Math.Min(gy * step, oh - 1) * ow + Math.Min(gx * step, ow - 1);
                if (va[k] != vb[k] && (hi < 0 || gx > hi + fade || gx < lo - fade)) zero[gy * NX + gx] = true;   // only one lens, far from the strip
            }
        }
        // Fill: each free point becomes the average of its neighbours, many times (smooth surface through the measures).
        for (int g = 0; g < N; g++) if (!keep[g]) { du[g] = 0; dv[g] = 0; }
        for (int it = 0; it < 3000; it++)
            for (int gy = 0; gy < NY; gy++) for (int gx = 0; gx < NX; gx++)
            {
                int g = gy * NX + gx; if (keep[g] || zero[g]) continue;
                double sx = 0, sy = 0; int c = 0;
                if (gx > 0) { sx += du[g - 1]; sy += dv[g - 1]; c++; }
                if (gx < NX - 1) { sx += du[g + 1]; sy += dv[g + 1]; c++; }
                if (gy > 0) { sx += du[g - NX]; sy += dv[g - NX]; c++; }
                if (gy < NY - 1) { sx += du[g + NX]; sy += dv[g + NX]; c++; }
                du[g] = sx / c; dv[g] = sy / c;
            }
        int kept = 0; foreach (var b in keep) if (b) kept++;
        Log("panoramica 2: ajuste fino " + measured + " medidas, " + kept + " buenas, corrimiento maximo " + maxShift.ToString("0") + " px, grilla " + NX + "x" + NY);
        // Seam: a path from top to bottom through the shared strip where both lenses (B already bent) look most alike,
        // so whatever still does not fit is left out of sight. Dynamic programming, one step left/right per row at most.
        Func<double[], double, double, double> grid = (f, x, y) =>
        {
            double gx = Math.Max(0, Math.Min(NX - 1.001, x / step)), gy = Math.Max(0, Math.Min(NY - 1.001, y / step));
            int x0 = (int)gx, y0 = (int)gy, k = y0 * NX + x0; double fx = gx - x0, fy = gy - y0;
            return (f[k] * (1 - fx) + f[k + 1] * fx) * (1 - fy) + (f[k + NX] * (1 - fx) + f[k + NX + 1] * fx) * fy;
        };
        const double INF = 1e18;
        var costPrev = new double[ow]; var costCur = new double[ow]; var from = new int[ow * oh]; var D = new double[ow];
        bool started = false;
        for (int y = 0; y < oh; y++)
        {
            bool any = false;
            for (int x = 0; x < ow; x++)
            {
                D[x] = INF;
                double hx = grid(du, x, y) / 2, hy = grid(dv, x, y) / 2;
                double axp = x - hx, ayp = y - hy, sx = x + hx, sy = y + hy;
                if (axp < 0 || ayp < 0 || axp >= ow - 1 || ayp >= oh - 1 || sx < 0 || sy < 0 || sx >= ow - 1 || sy >= oh - 1) continue;
                int ka = (int)ayp * ow + (int)axp; if (!va[ka] || !va[ka + 1] || !va[ka + ow] || !va[ka + ow + 1]) continue;
                int k = (int)sy * ow + (int)sx; if (!vb[k] || !vb[k + 1] || !vb[k + ow] || !vb[k + ow + 1]) continue;
                D[x] = Math.Abs(Bilinear(ca, ow, axp, ayp) - Bilinear(cb, ow, sx, sy)); any = true;
            }
            for (int x = 0; x < ow; x++)
            {
                if (!started) { costCur[x] = D[x]; from[y * ow + x] = x; continue; }
                int bi = x; double bv = costPrev[x];
                if (x > 0 && costPrev[x - 1] < bv) { bv = costPrev[x - 1]; bi = x - 1; }
                if (x < ow - 1 && costPrev[x + 1] < bv) { bv = costPrev[x + 1]; bi = x + 1; }
                costCur[x] = D[x] >= INF || bv >= INF ? (any ? INF : bv) : D[x] + bv; from[y * ow + x] = bi;
            }
            if (any) started = true;
            var tmp = costPrev; costPrev = costCur; costCur = tmp;
        }
        var seamPx = new double[oh]; int sxEnd = -1; double sv = INF;
        for (int x = 0; x < ow; x++) if (costPrev[x] < sv) { sv = costPrev[x]; sxEnd = x; }
        int nS = 0; double[] seamU = null;
        if (sxEnd >= 0)
        {
            int cx = sxEnd;
            for (int y = oh - 1; y >= 0; y--) { seamPx[y] = cx; cx = from[y * ow + cx]; }
            nS = 64; seamU = new double[nS];
            for (int k = 0; k < nS; k++)
            {
                int y = k * (oh - 1) / (nS - 1); double sum = 0; int c = 0;
                for (int j = Math.Max(0, y - 4); j <= Math.Min(oh - 1, y + 4); j++) { sum += seamPx[j]; c++; }
                seamU[k] = (sum / c + 0.5) / ow;
            }
        }
        Log("panoramica 2: union " + (nS > 0 ? "por el camino mas parecido" : "en el medio"));
        var r = new double[4 + 2 * N + 1 + nS]; r[0] = -NX; r[1] = NY;   // minus: both lenses take half r[2] = (double)step / ow; r[3] = (double)step / oh;
        for (int g = 0; g < N; g++) { r[4 + g] = du[g] / ow; r[4 + N + g] = dv[g] / oh; }
        r[4 + 2 * N] = nS; for (int k = 0; k < nS; k++) r[5 + 2 * N + k] = seamU[k];
        return r;
    }

    static double Bilinear(double[] img, int w, double x, double y)
    {
        int x0 = (int)x, y0 = (int)y; double fx = x - x0, fy = y - y0; int k = y0 * w + x0;
        return (img[k] * (1 - fx) + img[k + 1] * fx) * (1 - fy) + (img[k + w] * (1 - fx) + img[k + w + 1] * fx) * fy;
    }

    // True when both lenses take half of the fine bending (older calibrations bend lens B only).
    static bool FieldBoth(double[] c) { return c.Length >= 13 && c[9] < 0; }

    // Seam column (0 to 1) at height v of the picture, or -1 when the calibration has none.
    static double SeamAt(double[] c, double v)
    {
        if (c.Length < 13) return -1;
        int N = (int)Math.Abs(c[9]) * (int)c[10], o = 13 + 2 * N; if (c.Length <= o) return -1;
        int nS = (int)c[o]; if (nS < 2 || c.Length < o + 1 + nS) return -1;
        double t = Math.Max(0, Math.Min(nS - 1.001, v * (nS - 1))); int i = (int)t; double f = t - i;
        return c[o + 1 + i] * (1 - f) + c[o + 2 + i] * f;
    }

    // Fine bending at a point of the picture (u, v from 0 to 1): shift of lens B as parts of the picture size.
    static void FieldAt(double[] c, double u, double v, out double du, out double dv)
    {
        du = dv = 0;
        if (c.Length < 13) return;
        int NX = (int)Math.Abs(c[9]), NY = (int)c[10], N = NX * NY; if (c.Length < 13 + 2 * N || NX < 2 || NY < 2) return;
        double gx = Math.Max(0, Math.Min(NX - 1.001, u / c[11])), gy = Math.Max(0, Math.Min(NY - 1.001, v / c[12]));
        int x0 = (int)gx, y0 = (int)gy; double fx = gx - x0, fy = gy - y0; int k = y0 * NX + x0;
        Func<int, double> at = o => (c[o + k] * (1 - fx) + c[o + k + 1] * fx) * (1 - fy) + (c[o + k + NX] * (1 - fx) + c[o + k + NX + 1] * fx) * fy;
        du = at(13); dv = at(13 + N);
    }

    static void PanoTest(string inFile, string outBase, string prevFile)
    {
        LogFile = outBase + ".log";
        var inv = System.Globalization.CultureInfo.InvariantCulture;
        double[] prev = null;
        if (prevFile != "-") { var f = File.ReadAllText(prevFile).Trim().Split(' '); prev = new double[f.Length]; for (int i = 0; i < f.Length; i++) prev[i] = double.Parse(f[i], inv); }
        using (var src = new Bitmap(inFile))
        {
            string err;
            var sw = System.Diagnostics.Stopwatch.StartNew();
            var c1 = Calibrate(src, out err);
            if (c1 == null) { Log("modo 1: " + err); return; }
            var c2 = Calibrate2(src, c1, prev, out err);
            Log("calculo " + sw.ElapsedMilliseconds + " ms" + (c2 == null ? ", modo 2: " + err : ""));
            if (c2 != null) File.WriteAllText(outBase + "_c2.txt", Nums(c2));
            // The picture as an I420 frame, like the SDK gives it.
            int w = src.Width & ~1, h = src.Height & ~3;
            var yuv = new byte[w * h * 3 / 2];
            using (var b = src.Clone(new Rectangle(0, 0, w, h), System.Drawing.Imaging.PixelFormat.Format24bppRgb))
            {
                var d = b.LockBits(new Rectangle(0, 0, w, h), System.Drawing.Imaging.ImageLockMode.ReadOnly, b.PixelFormat);
                var raw = new byte[d.Stride * h]; Marshal.Copy(d.Scan0, raw, 0, raw.Length); b.UnlockBits(d);
                for (int y = 0; y < h; y++) for (int x = 0; x < w; x++)
                {
                    int i = y * d.Stride + x * 3; double B = raw[i], G = raw[i + 1], R = raw[i + 2];
                    yuv[y * w + x] = (byte)Math.Max(0, Math.Min(255, 16 + 0.257 * R + 0.504 * G + 0.098 * B));
                    if ((x & 1) == 0 && (y & 1) == 0)
                    {
                        int k = (y / 2) * (w / 2) + x / 2;
                        yuv[w * h + k] = (byte)Math.Max(0, Math.Min(255, 128 - 0.148 * R - 0.291 * G + 0.439 * B));
                        yuv[w * h + w * h / 4 + k] = (byte)Math.Max(0, Math.Min(255, 128 + 0.439 * R - 0.368 * G - 0.071 * B));
                    }
                }
            }
            var pin = GCHandle.Alloc(yuv, GCHandleType.Pinned);
            foreach (var mode in new[] { 1, 2 })
            {
                if (mode == 2 && c2 == null) continue;
                var v = new PanoView { Cal = new PanoCal { Mode = mode, C1 = c1, C2 = c2 } };
                v.Frame(pin.AddrOfPinnedObject(), w, h);
                File.WriteAllBytes(outBase + "_" + mode + ".jpg", v.Jpeg(1400));
                if (mode == 2) File.WriteAllBytes(outBase + "_grid.jpg", v.GridJpeg(1400));
            }
            pin.Free();
        }
    }

    static double[] NelderMead(Func<double[], double> fn, double[] x0, double[] step, int iters, out double bestV)
    {
        int n = x0.Length; var pts = new double[n + 1][]; var v = new double[n + 1];
        for (int i = 0; i <= n; i++) { pts[i] = (double[])x0.Clone(); if (i > 0) pts[i][i - 1] += step[i - 1]; v[i] = fn(pts[i]); }
        for (int it = 0; it < iters; it++)
        {
            var idx = new int[n + 1]; for (int i = 0; i <= n; i++) idx[i] = i;
            Array.Sort(idx, (a, b) => v[a].CompareTo(v[b]));
            var sp = new double[n + 1][]; var sv = new double[n + 1];
            for (int i = 0; i <= n; i++) { sp[i] = pts[idx[i]]; sv[i] = v[idx[i]]; }
            pts = sp; v = sv;
            var c = new double[n]; for (int i = 0; i < n; i++) for (int j = 0; j < n; j++) c[j] += pts[i][j] / n;
            var w = pts[n];
            var refl = new double[n]; for (int j = 0; j < n; j++) refl[j] = c[j] + (c[j] - w[j]); double fr = fn(refl);
            if (fr < v[0])
            {
                var ex = new double[n]; for (int j = 0; j < n; j++) ex[j] = c[j] + 2 * (c[j] - w[j]); double fe = fn(ex);
                if (fe < fr) { pts[n] = ex; v[n] = fe; } else { pts[n] = refl; v[n] = fr; }
            }
            else if (fr < v[n - 1]) { pts[n] = refl; v[n] = fr; }
            else
            {
                var co = new double[n]; for (int j = 0; j < n; j++) co[j] = c[j] + 0.5 * (w[j] - c[j]); double fc = fn(co);
                if (fc < v[n]) { pts[n] = co; v[n] = fc; }
                else for (int i = 1; i <= n; i++) { for (int j = 0; j < n; j++) pts[i][j] = pts[0][j] + 0.5 * (pts[i][j] - pts[0][j]); v[i] = fn(pts[i]); }
            }
        }
        int bi = 0; for (int i = 1; i <= n; i++) if (v[i] < v[bi]) bi = i;
        bestV = v[bi]; return pts[bi];
    }

    class Gray { public int W, H; public double[] G; }

    // Gray picture of part of a bitmap, w pixels wide, then turned into edge strength.
    static Gray Edges(Bitmap src, Rectangle part, int w)
    {
        int h = Math.Max(3, part.Height * w / part.Width);
        var g = new Gray { W = w, H = h, G = new double[w * h] };
        using (var b = new Bitmap(w, h, System.Drawing.Imaging.PixelFormat.Format24bppRgb))
        {
            using (var gr = Graphics.FromImage(b)) { gr.InterpolationMode = System.Drawing.Drawing2D.InterpolationMode.HighQualityBilinear; gr.DrawImage(src, new Rectangle(0, 0, w, h), part, GraphicsUnit.Pixel); }
            var d = b.LockBits(new Rectangle(0, 0, w, h), System.Drawing.Imaging.ImageLockMode.ReadOnly, b.PixelFormat);
            var raw = new byte[d.Stride * h]; Marshal.Copy(d.Scan0, raw, 0, raw.Length); b.UnlockBits(d);
            var l = new double[w * h];
            for (int y = 0; y < h; y++) for (int x = 0; x < w; x++) { int i = y * d.Stride + x * 3; l[y * w + x] = 0.114 * raw[i] + 0.587 * raw[i + 1] + 0.299 * raw[i + 2]; }
            for (int y = 1; y < h - 1; y++) for (int x = 1; x < w - 1; x++)
            {
                int i = y * w + x; double gx = l[i + 1] - l[i - 1], gy = l[i + w] - l[i - w];
                g.G[i] = Math.Sqrt(gx * gx + gy * gy);
            }
        }
        return g;
    }

    // How well B (size s, at dx, dy in A's pixels) matches A where both have picture: correlation from -1 to 1.
    // Rows near the top and bottom are left out (the date and the camera name are written there).
    static double Match(Gray A, Gray B, double s, double dx, double dy, double minPart)
    {
        int n = 0, m = (int)(A.H * 0.12); double sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0;
        for (int y = m; y < A.H - m; y++)
        {
            double by = (y - dy) / s; if (by < m || by >= B.H - m) continue;
            int bRow = (int)by * B.W;
            for (int x = 2; x < A.W - 2; x++)
            {
                double bx = (x - dx) / s; if (bx < 2 || bx >= B.W - 2) continue;
                double a = A.G[y * A.W + x], b = B.G[bRow + (int)bx];
                n++; sa += a; sb += b; saa += a * a; sbb += b * b; sab += a * b;
            }
        }
        if (n < minPart * A.W * A.H) return -2;
        double cov = sab / n - sa / n * sb / n, va = saa / n - sa / n * sa / n, vb = sbb / n - sb / n * sb / n;
        return va > 0 && vb > 0 ? cov / Math.Sqrt(va * vb) : -2;
    }

    // Returns {s, dx, dy} (units: A's width), or null with the reason.
    static double[] Calibrate(Bitmap src, out string err)
    {
        err = null;
        int hh = src.Height / 2;
        var rA = new Rectangle(0, 0, src.Width, hh); var rB = new Rectangle(0, hh, src.Width, hh);
        var A = Edges(src, rA, 96); var B = Edges(src, rB, 96);
        double best = -3, bs = 1, bdx = 0, bdy = 0;
        for (double s = 0.7; s <= 1.4; s *= 1.04)
            for (double dx = -A.W * 0.92; dx <= A.W * 0.92; dx++)
                for (double dy = -A.H * 0.4; dy <= A.H * 0.4; dy++)
                {
                    double v = Match(A, B, s, dx, dy, 0.12);
                    if (v > best) { best = v; bs = s; bdx = dx / A.W; bdy = dy / A.W; }
                }
        Log("panoramica: busqueda gruesa " + best.ToString("0.00"));
        if (best < 0.25) { err = "las dos lentes no ven una parte en comun (la camara que se mueve apunta a otro lado?)"; return null; }
        A = Edges(src, rA, 320); B = Edges(src, rB, 320);
        double fbest = -3, fs = bs, fdx = bdx, fdy = bdy;
        for (double s = bs * 0.95; s <= bs * 1.05; s *= 1.005)
            for (double dx = bdx * A.W - 5; dx <= bdx * A.W + 5; dx += 0.5)
                for (double dy = bdy * A.W - 5; dy <= bdy * A.W + 5; dy += 0.5)
                {
                    double v = Match(A, B, s, dx, dy, 0.08);
                    if (v > fbest) { fbest = v; fs = s; fdx = dx / A.W; fdy = dy / A.W; }
                }
        Log("panoramica: busqueda fina " + fbest.ToString("0.00"));
        return new[] { fs, fdx, fdy };
    }

    const string Page = @"<!doctype html><html><head><meta charset='utf-8'><meta name='viewport' content='width=device-width,initial-scale=1'>
<title>Camaras</title><style>
body{margin:0;background:#111;color:#eee;font-family:sans-serif}
header{position:sticky;top:0;background:#111;padding:10px;display:flex;gap:10px;align-items:center;z-index:1}
h1{font-size:18px;margin:0;flex:1}
button{font-size:16px;padding:10px 14px;border:0;border-radius:8px;background:#333;color:#eee}
#grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:10px;padding:10px}
.cam{background:#000;border-radius:8px;overflow:hidden;border:3px solid transparent}
.cam.big{border-color:#fc0}
.cam img{width:100%;aspect-ratio:16/9;object-fit:cover;display:block;background:#222;touch-action:none;-webkit-user-drag:none;user-select:none}
.cam.target{border-color:#4af}.cam.drag{opacity:.5}
.cam.wide{grid-column:1/-1}.cam.wide img{aspect-ratio:16/5;object-fit:contain}
#ghost{position:fixed;width:140px;pointer-events:none;opacity:.8;border-radius:6px;display:none;z-index:9}
p.tip{color:#888;font-size:13px;margin:0 10px}
.row{display:flex;justify-content:space-between;align-items:center;padding:6px}
.hd{background:#333}.hd.on{background:#c00;color:#fff;font-weight:bold}
#pano button.on{background:#fc0;color:#000;font-weight:bold}
#ptz{display:none;align-items:center;justify-content:center;gap:24px;padding:10px}
#pad{width:170px;height:170px;border-radius:50%;background:#222;border:2px solid #555;position:relative;touch-action:none}
#knob{width:60px;height:60px;border-radius:50%;background:#fc0;position:absolute;left:55px;top:55px;pointer-events:none}
#zoom{display:flex;flex-direction:column;gap:14px}#zoom button{width:64px;height:64px;font-size:28px;touch-action:none}
</style></head><body>
<header><h1>Camaras</h1><span id=net></span><button data-all=1>Ver todas</button></header>
<div id=pano style='display:none;padding:0 10px 6px'>Camaras 3 y 4 juntas: <button data-pano=0>No</button> <button data-pano=1>Simple</button> <button data-pano=2>Avanzada</button><p class=tip id=gridtip style='display:none;margin:4px 0 0'>En la panoramica: grilla celeste = lente de la camara 3, naranja = camara 4.</p></div>
<p class=tip>Tocar una camara: grande / todas. Arrastrarla sobre otra: cambian de lugar.</p>
<p class=tip id=msg style='color:#fc0'></p>
<div id=ptz><div id=pad><div id=knob></div></div><div id=zoom><button id=zin>+</button><button id=zout>&minus;</button></div></div>
<div id=grid></div><img id=ghost>
<script>
var st={big:-1,cams:[]};
// ONE connection that stays open (WebSocket) for everything; if it drops, connect again at once.
// Buttons change on the phone right away; the TV follows when the command arrives.
var ws=null,queue=[],lastMsg=0,net=document.getElementById('net');
// Opening sometimes gets stuck on the PC's Wi-Fi: if it is not open in 1.5 s, drop it and try again.
function connect(){
 var w=new WebSocket('ws://'+location.host+'/ws');ws=w;w.binaryType='arraybuffer';
 var stuck=setTimeout(function(){if(w.readyState!=1)w.close()},1500);
 w.onopen=function(){clearTimeout(stuck);net.textContent='';lastMsg=Date.now();while(queue.length)w.send(queue.shift())};
 w.onmessage=function(e){lastMsg=Date.now();
  if(typeof e.data=='string'){draw(JSON.parse(e.data));return}
  var b=new Uint8Array(e.data),im=document.querySelectorAll('.cam img')[b[0]];if(!im)return;
  var old=im.src;im.src=URL.createObjectURL(new Blob([b.subarray(1)],{type:'image/jpeg'}));if(old.indexOf('blob:')==0)URL.revokeObjectURL(old)};
 w.onclose=function(){clearTimeout(stuck);if(ws!=w)return;net.textContent='reconectando...';setTimeout(connect,200)};
}
function cmd(c){queue=queue.filter(function(q){return q.split(' ')[0]+q.split(' ')[1]!=c.split(' ')[0]+c.split(' ')[1]});
 if(ws&&ws.readyState==1)ws.send(c);else queue.push(c)}
// The PC sends its state every 3 s: 6 s of silence means the connection is dead, so drop it and connect again.
setInterval(function(){if(ws&&ws.readyState==1&&Date.now()-lastMsg>6000)ws.close()},1000);
function draw(s){st=s;var g=document.getElementById('grid');
 while(g.children.length<s.cams.length){var i=g.children.length,d=document.createElement('div');d.className='cam';
  d.innerHTML='<img data-i='+i+'><div class=row><span>Camara '+(i+1)+'</span><button class=hd data-hd='+i+'>HD</button></div>';g.appendChild(d)}
 while(g.children.length>s.cams.length)g.removeChild(g.lastChild);
 s.cams.forEach(function(c,i){var d=g.children[i];d.classList.toggle('big',i==s.big);d.querySelector('.hd').classList.toggle('on',c.hd);
  d.classList.toggle('wide',/\.3$/.test(c.k))});
 var pb=document.getElementById('pano');pb.style.display=s.split?'':'none';
 [].forEach.call(pb.querySelectorAll('button'),function(b){b.classList.toggle('on',+b.dataset.pano==(+s.pano||0))});
 document.getElementById('msg').textContent=s.msg||'';
 document.getElementById('gridtip').style.display=s.pano&&!s.msg?'':'none';
 var c=s.cams[s.big];document.getElementById('ptz').style.display=c?'flex':'none'}
// Joystick pad: the knob follows the finger; direction and distance from the center = direction and speed.
var pad=document.getElementById('pad'),knob=document.getElementById('knob'),padOn=false,lastSend=0;
function padMove(e){var r=pad.getBoundingClientRect(),dx=e.clientX-r.left-r.width/2,dy=e.clientY-r.top-r.height/2,m=Math.hypot(dx,dy),max=r.width/2-30;
 if(m>max){dx=dx*max/m;dy=dy*max/m}knob.style.left=(55+dx)+'px';knob.style.top=(55+dy)+'px';
 if(Date.now()-lastSend>150&&Math.hypot(dx,dy)>8){lastSend=Date.now();cmd('ptz '+st.big+' '+Math.round(dx*2.5)+' '+Math.round(dy*2.5))}}
pad.addEventListener('pointerdown',function(e){padOn=true;pad.setPointerCapture(e.pointerId);padMove(e);e.preventDefault()});
pad.addEventListener('pointermove',function(e){if(padOn)padMove(e)});
// Keep sending while the finger is held still (the picture of a camera without PTZ moves one step per command).
var padLast=null;setInterval(function(){if(padOn&&padLast)padMove(padLast)},150);
pad.addEventListener('pointermove',function(e){padLast=e});pad.addEventListener('pointerdown',function(e){padLast=e});
function padEnd(){if(!padOn)return;padOn=false;padLast=null;knob.style.left='55px';knob.style.top='55px';cmd('ptzstop')}
pad.addEventListener('pointerup',padEnd);pad.addEventListener('pointercancel',padEnd);
// Zoom buttons (digital zoom on the TV): one step per tap, repeats every 250 ms while held.
[['zin',1],['zout',-1]].forEach(function(z){var b=document.getElementById(z[0]),rep=null;
 function step(){ws&&ws.readyState==1&&ws.send('zoom '+st.big+' '+z[1])}
 function end(){clearInterval(rep);rep=null}
 b.addEventListener('pointerdown',function(e){e.preventDefault();b.setPointerCapture(e.pointerId);step();end();rep=setInterval(step,250)});
 b.addEventListener('pointerup',end);b.addEventListener('pointercancel',end)});
function setBig(i){st.big=i;draw(st);cmd('big '+i)}
// Tap = big; drag onto another camera = swap (pointer events work with fingers and mouse).
var drag=null,ghost=document.getElementById('ghost');
function camAt(x,y){var e=document.elementFromPoint(x,y);while(e&&!(e.classList&&e.classList.contains('cam')))e=e.parentNode;return e}
document.addEventListener('pointerdown',function(e){if(!e.target.dataset||e.target.dataset.i==null)return;e.preventDefault();
 drag={i:+e.target.dataset.i,x:e.clientX,y:e.clientY,moved:false,el:e.target.parentNode,to:null};});
document.addEventListener('pointermove',function(e){if(!drag)return;
 if(!drag.moved&&Math.abs(e.clientX-drag.x)+Math.abs(e.clientY-drag.y)>15){drag.moved=true;drag.el.classList.add('drag');ghost.src=drag.el.querySelector('img').src;ghost.style.display='block'}
 if(!drag.moved)return;ghost.style.left=(e.clientX-70)+'px';ghost.style.top=(e.clientY-40)+'px';
 var c=camAt(e.clientX,e.clientY);if(drag.to&&drag.to!=c)drag.to.classList.remove('target');drag.to=c&&c!=drag.el?c:null;if(drag.to)drag.to.classList.add('target')});
function endDrag(e){if(!drag)return;var d=drag;drag=null;ghost.style.display='none';d.el.classList.remove('drag');if(d.to)d.to.classList.remove('target');
 if(!d.moved){setBig(st.big==d.i?-1:d.i);return}
 if(d.to){var j=[].indexOf.call(d.to.parentNode.children,d.to),a=d.el.querySelector('img'),b=d.to.querySelector('img'),t=a.src;a.src=b.src;b.src=t;
  var x=st.cams[d.i];st.cams[d.i]=st.cams[j];st.cams[j]=x;draw(st);cmd('order '+st.cams.map(function(c){return c.k}).join(','))}}
document.addEventListener('pointerup',endDrag);document.addEventListener('pointercancel',endDrag);
document.addEventListener('click',function(e){var b=e.target;if(!b.dataset)return;
 if(b.dataset.pano!=null){var m=+b.dataset.pano;if(m==0&&!st.pano)return;st.pano=m;st.msg=m?'Calculando la panoramica...':'';draw(st);cmd('pano '+m);return}
 if(b.dataset.all){setBig(-1);return}
 if(b.dataset.hd==null)return;var i=+b.dataset.hd,on=!st.cams[i].hd;st.cams[i].hd=on;draw(st);cmd('hd '+i+' '+(on?1:0))});
connect();
</script></body></html>";

    void Status(string s)
    {
        Log(s);
        BeginInvoke((MethodInvoker)delegate { status.Text = s; });
    }

    // Plays every channel for a few seconds without showing it and keeps the ones that send video.
    // Channels in `skip` are already on screen and are not opened again (the NVR limits how many streams it sends).
    // The others are opened 8 at a time, so the NVR never sends too many streams at once.
    List<int> DiscoverCameras(int nChan, HashSet<int> skip)
    {
        var found = new List<int>();
        int tried = 0;
        var todo = new List<int>();
        for (int ch = 0; ch < Math.Min(nChan, 64); ch++) if (!skip.Contains(ch)) todo.Add(ch);
        for (int i = 0; i < todo.Count; i += 8)
        {
            var plays = new Dictionary<long, int>();
            for (int k = i; k < Math.Min(i + 8, todo.Count); k++)
            {
                long h = Native.CLIENT_RealPlayEx(login, todo[k], IntPtr.Zero, 3);
                if (h == 0) continue;
                lock (bytesPerPlay) bytesPerPlay.Remove(h);   // the number may be reused from a stopped stream
                Native.CLIENT_SetRealDataCallBackEx2(h, dataCb, IntPtr.Zero, 1);
                plays[h] = todo[k];
            }
            Thread.Sleep(int.Parse(C("busqueda_segundos", "6")) * 1000);
            foreach (var kv in plays)
            {
                long b = BytesOf(kv.Key);
                Native.CLIENT_StopRealPlayEx(kv.Key);
                lock (bytesPerPlay) bytesPerPlay.Remove(kv.Key);
                if (b > 20000) found.Add(kv.Value);
            }
            tried += plays.Count;
        }
        found.Sort();
        Log("busqueda: " + tried + " canales probados, con video nuevo: " + string.Join(",", found));
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
                // The NVR is registered on one server only: on the others it never gets online, so skip them at once.
                if (!WaitFor("NVR online", 10, () => Native.P2P_DeviceStatus(0, h, sn))) continue;
                // A tunnel opens in about 1 s (sometimes up to 9 s). When it fails, the library says so (state 1)
                // after about 10 s: then try again right away on the same server, instead of restarting everything.
                for (int attempt = 1; attempt <= 3 && !ok; attempt++)
                {
                    localPort = 0;
                    r = Native.P2P_Connect(0, h, sn, 37777, ref localPort, user, pass, "", "");
                    Log("P2P_Connect intento " + attempt + " -> " + r + " localPort " + localPort);
                    if (r != 0 || localPort <= 0) break;
                    int lp = localPort;
                    if (WaitFor("tunel" + (attempt > 1 ? " (intento " + attempt + ")" : ""), 12, () => Native.P2P_QueryChannelStatus(0, h, lp), 1)) ok = true;
                }
                if (ok) break;
            }
            if (!ok) { RetryLater("No pude abrir el tunel P2P", "tunel fallo"); return; }

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
            if (login == 0 && nErr == 1) { Status("Clave del NVR incorrecta. Borra " + PassFile + " y abri de nuevo."); return; }
            if (login == 0) { RetryLater("No pude entrar al NVR (error " + nErr + ")", "login fallo"); return; }

            // 3. Show the cameras found last time right away, then look for cameras again
            //    (a camera added to the NVR appears; the list is saved for the next start).
            List<int> cached = new List<int>();
            try { foreach (var c in File.ReadAllText(CacheFile).Split(',')) { int ch; if (int.TryParse(c.Trim(), out ch)) cached.Add(ch); } } catch { }
            if (cached.Count > 0) Invoke((MethodInvoker)delegate { BuildTiles(cached); });
            else Status("Buscando camaras...");
            var found = DiscoverCameras(nChan, new HashSet<int>(cached));
            // Cameras already on screen count as found if their square got video meanwhile.
            Invoke((MethodInvoker)delegate
            {
                foreach (var t in tiles) if (BytesOf(t.Play) > 20000 && !found.Contains(t.Ch)) found.Add(t.Ch);
                foreach (var ch in gotVideo) if (!found.Contains(ch)) found.Add(ch);
            });
            found.Sort();
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
    // Waits until check() gives 0 (ready). Gives up after secs, or at once when it gives failState.
    bool WaitFor(string what, int secs, Func<int> check, int failState = -99)
    {
        var end = DateTime.Now.AddSeconds(secs);
        int last = -99;
        while (DateTime.Now < end)
        {
            int s = check();
            if (s != last) { Log(what + ": estado " + s); last = s; }
            if (s == 0) return true;
            if (s == failState) { Log(what + ": fallo"); return false; }
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
    void RetryLater(string msg, string why)
    {
        int wait = retryMs;
        // Usually the Wi-Fi is the problem: tell what to do, in big letters.
        Status(msg + ", reintento en " + (wait / 1000) + " s");
        BeginInvoke((MethodInvoker)delegate {
            status.ForeColor = Color.White; status.Font = new Font("Segoe UI", 28);
            status.Text = "No hay conexion con las camaras.\n\nProba esto:\n  1. Conectar la PC a la otra red Wi-Fi.\n  2. O reiniciar el router: desenchufarlo y volver a enchufarlo.\n\n"
                + msg + ". Reintento en " + (wait / 1000) + " s.";
        });
        Thread.Sleep(wait);
        BeginInvoke((MethodInvoker)delegate { Restart(why, Math.Min(wait * 2, 15000)); });
    }

    static void Main(string[] args)
    {
        // Set by Restart after a failed connection: how long to wait if this try fails too.
        if (args.Length == 2 && args[0] == "--espera") int.TryParse(args[1], out retryMs);
        // Test: Camaras.exe --qr file.png text   (writes the QR as an image and exits)
        // Test: Camaras.exe --pano picture.jpg out (prev numbers file or -)  works out both panoramas from a picture
        // of the split channel and writes out_1.jpg, out_2.jpg, out_grid.jpg; the log goes to out.log.
        if (args.Length >= 3 && args[0] == "--pano") { PanoTest(args[1], args[2], args.Length > 3 ? args[3] : "-"); return; }
        if (args.Length == 3 && args[0] == "--qr") { Qr.ToBitmap(Qr.Encode(args[2]), 8).Save(args[1], System.Drawing.Imaging.ImageFormat.Png); return; }
        // Draw in real screen pixels. Without this, with Windows zoom (e.g. 150%) Windows draws the video smaller
        // and then stretches the whole window up, which makes every picture blurry.
        Win.SetProcessDPIAware();
        Application.EnableVisualStyles();
        // Only one copy: if one already runs, bring it to the front and quit.
        bool mine;
        single = new Mutex(false, "DahuaP2PViewer");
        try { mine = single.WaitOne(3000); } catch (AbandonedMutexException) { mine = true; }   // abandoned = the old copy crashed
        if (!mine)
        {
            var me = System.Diagnostics.Process.GetCurrentProcess();
            foreach (var pr in System.Diagnostics.Process.GetProcessesByName(me.ProcessName))
                if (pr.Id != me.Id && pr.MainWindowHandle != IntPtr.Zero) { Win.ShowWindowAsync(pr.MainWindowHandle, 3); Win.SetForegroundWindow(pr.MainWindowHandle); }
            return;
        }
        Log("---- inicio");
        // This copy owns the lock, so any other running copy is a leftover that hung while closing: end it.
        var self = System.Diagnostics.Process.GetCurrentProcess();
        foreach (var pr in System.Diagnostics.Process.GetProcessesByName(self.ProcessName))
            if (pr.Id != self.Id) { Log("cierro copia vieja colgada " + pr.Id); try { pr.Kill(); } catch (Exception ex) { Log("no pude: " + ex.Message); } }
        if (!EnsurePassword()) return;
        Application.Run(new CamForm());
        HardExit();
    }
}

// Minimal QR code generator (byte mode, error correction level M, versions 1-6: up to 106 bytes).
// Follows the QR standard (ISO 18004); same steps as Nayuki's public-domain-style reference implementation.
static class Qr
{
    // Per version 1-6, level M: total codewords, EC codewords per block, number of blocks.
    static readonly int[] Total = { 0, 26, 44, 70, 100, 134, 172 };
    static readonly int[] EcLen = { 0, 10, 16, 26, 18, 24, 16 };
    static readonly int[] Blocks = { 0, 1, 1, 1, 2, 2, 4 };

    public static bool[,] Encode(string text)
    {
        byte[] data = Encoding.UTF8.GetBytes(text);
        int ver = 1;
        while (ver <= 6 && (Total[ver] - EcLen[ver] * Blocks[ver]) * 8 < 4 + 8 + data.Length * 8) ver++;
        if (ver > 6) throw new ArgumentException("texto muy largo para el QR");
        int dataCap = Total[ver] - EcLen[ver] * Blocks[ver];
        // Bits: mode 0100 (bytes), 8-bit length, the bytes, terminator, then pad bytes.
        var bits = new List<bool>();
        Action<int, int> add = (v, n) => { for (int i = n - 1; i >= 0; i--) bits.Add(((v >> i) & 1) != 0); };
        add(4, 4); add(data.Length, 8); foreach (var b in data) add(b, 8);
        add(0, Math.Min(4, dataCap * 8 - bits.Count));
        while (bits.Count % 8 != 0) bits.Add(false);
        for (int pad = 0xEC; bits.Count < dataCap * 8; pad ^= 0xEC ^ 0x11) add(pad, 8);
        var cw = new byte[dataCap];
        for (int i = 0; i < bits.Count; i++) if (bits[i]) cw[i >> 3] |= (byte)(1 << (7 - (i & 7)));
        // Error correction per block, then interleave.
        int nb = Blocks[ver], ec = EcLen[ver], blen = dataCap / nb;
        var div = RsDivisor(ec);
        var dataBlocks = new List<byte[]>(); var ecBlocks = new List<byte[]>();
        for (int b = 0; b < nb; b++)
        {
            var d = new byte[blen]; Array.Copy(cw, b * blen, d, 0, blen);
            dataBlocks.Add(d); ecBlocks.Add(RsRemainder(d, div));
        }
        var all = new List<byte>();
        for (int i = 0; i < blen; i++) foreach (var d in dataBlocks) all.Add(d[i]);
        for (int i = 0; i < ec; i++) foreach (var e in ecBlocks) all.Add(e[i]);

        int size = 17 + 4 * ver;
        var m = new bool[size, size]; var fn = new bool[size, size];   // [y, x]
        Action<int, int, bool> setF = (x, y, v) => { m[y, x] = v; fn[y, x] = true; };
        // Timing patterns.
        for (int i = 0; i < size; i++) { setF(6, i, i % 2 == 0); setF(i, 6, i % 2 == 0); }
        // Finder patterns with separators.
        foreach (var c in new[] { new[] { 3, 3 }, new[] { size - 4, 3 }, new[] { 3, size - 4 } })
            for (int dy = -4; dy <= 4; dy++) for (int dx = -4; dx <= 4; dx++)
            {
                int x = c[0] + dx, y = c[1] + dy;
                if (x < 0 || y < 0 || x >= size || y >= size) continue;
                int dist = Math.Max(Math.Abs(dx), Math.Abs(dy));
                setF(x, y, dist != 2 && dist != 4);
            }
        // Alignment pattern (versions 2-6 have one, at (pos, pos)).
        if (ver >= 2)
        {
            int p = size - 7;
            for (int dy = -2; dy <= 2; dy++) for (int dx = -2; dx <= 2; dx++) setF(p + dx, p + dy, Math.Max(Math.Abs(dx), Math.Abs(dy)) != 1);
        }
        // Reserve the format areas (filled later) and the dark module.
        DrawFormat(m, fn, size, 0, setF);
        // Data, in the zigzag order.
        int bi = 0;
        for (int right = size - 1; right >= 1; right -= 2)
        {
            if (right == 6) right = 5;
            for (int vert = 0; vert < size; vert++)
                for (int j = 0; j < 2; j++)
                {
                    int x = right - j; bool up = ((right + 1) & 2) == 0; int y = up ? size - 1 - vert : vert;
                    if (!fn[y, x] && bi < all.Count * 8) { m[y, x] = ((all[bi >> 3] >> (7 - (bi & 7))) & 1) != 0; bi++; }
                }
        }
        // Try the 8 masks, keep the one with the lowest penalty.
        bool[,] best = null; int bestScore = int.MaxValue;
        for (int mask = 0; mask < 8; mask++)
        {
            var t = (bool[,])m.Clone();
            for (int y = 0; y < size; y++) for (int x = 0; x < size; x++)
                if (!fn[y, x] && MaskBit(mask, x, y)) t[y, x] = !t[y, x];
            Action<int, int, bool> setT = (x, y, v) => { t[y, x] = v; };
            DrawFormat(t, fn, size, mask, setT);
            int s = Penalty(t, size);
            if (s < bestScore) { bestScore = s; best = t; }
        }
        return best;
    }

    static bool MaskBit(int mask, int x, int y)
    {
        switch (mask)
        {
            case 0: return (x + y) % 2 == 0;
            case 1: return y % 2 == 0;
            case 2: return x % 3 == 0;
            case 3: return (x + y) % 3 == 0;
            case 4: return (x / 3 + y / 2) % 2 == 0;
            case 5: return x * y % 2 + x * y % 3 == 0;
            case 6: return (x * y % 2 + x * y % 3) % 2 == 0;
            default: return ((x + y) % 2 + x * y % 3) % 2 == 0;
        }
    }

    // Format info: level M (bits 00) + mask, BCH protected, written twice; plus the dark module.
    static void DrawFormat(bool[,] m, bool[,] fn, int size, int mask, Action<int, int, bool> set)
    {
        int data = (0 << 3) | mask, rem = data;
        for (int i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >> 9) * 0x537);
        int bits = ((data << 10) | rem) ^ 0x5412;
        Func<int, bool> b = i => ((bits >> i) & 1) != 0;
        for (int i = 0; i <= 5; i++) set(8, i, b(i));
        set(8, 7, b(6)); set(8, 8, b(7)); set(7, 8, b(8));
        for (int i = 9; i < 15; i++) set(14 - i, 8, b(i));
        for (int i = 0; i < 8; i++) set(size - 1 - i, 8, b(i));
        for (int i = 8; i < 15; i++) set(8, size - 15 + i, b(i));
        set(8, size - 8, true);
    }

    // Standard penalty rules: runs of 5+, 2x2 blocks, finder-like patterns, dark/light balance.
    static int Penalty(bool[,] m, int size)
    {
        int p = 0, dark = 0;
        for (int pass = 0; pass < 2; pass++)
            for (int a = 0; a < size; a++)
            {
                int run = 1;
                for (int c = 1; c < size; c++)
                {
                    bool cur = pass == 0 ? m[a, c] : m[c, a], prev = pass == 0 ? m[a, c - 1] : m[c - 1, a];
                    if (cur == prev) { run++; if (run == 5) p += 3; else if (run > 5) p++; } else run = 1;
                }
                for (int c = 0; c + 6 < size; c++)
                {
                    bool ok = true; bool[] pat = { true, false, true, true, true, false, true };
                    for (int k = 0; k < 7 && ok; k++) ok = (pass == 0 ? m[a, c + k] : m[c + k, a]) == pat[k];
                    if (ok) p += 40;
                }
            }
        for (int y = 0; y < size; y++) for (int x = 0; x < size; x++)
        {
            if (m[y, x]) dark++;
            if (x + 1 < size && y + 1 < size && m[y, x] == m[y, x + 1] && m[y, x] == m[y + 1, x] && m[y, x] == m[y + 1, x + 1]) p += 3;
        }
        p += Math.Abs(dark * 20 - size * size * 10) / (size * size) * 10;
        return p;
    }

    static int Mul(int x, int y)
    {
        int z = 0;
        for (int i = 7; i >= 0; i--) { z = (z << 1) ^ ((z >> 7) * 0x11D); z ^= ((y >> i) & 1) * x; }
        return z & 0xFF;
    }
    static byte[] RsDivisor(int degree)
    {
        var r = new byte[degree]; r[degree - 1] = 1; int root = 1;
        for (int i = 0; i < degree; i++)
        {
            for (int j = 0; j < degree; j++) { r[j] = (byte)Mul(r[j], root); if (j + 1 < degree) r[j] ^= r[j + 1]; }
            root = Mul(root, 2);
        }
        return r;
    }
    static byte[] RsRemainder(byte[] data, byte[] div)
    {
        var r = new byte[div.Length];
        foreach (var b in data)
        {
            int f = b ^ r[0];
            Array.Copy(r, 1, r, 0, r.Length - 1); r[r.Length - 1] = 0;
            for (int i = 0; i < r.Length; i++) r[i] ^= (byte)Mul(div[i], f);
        }
        return r;
    }

    // Draws the code black on white with a quiet zone of 4 modules.
    public static Bitmap ToBitmap(bool[,] q, int px)
    {
        int n = q.GetLength(0), side = (n + 8) * px;
        var bmp = new Bitmap(side, side);
        using (var g = Graphics.FromImage(bmp))
        {
            g.Clear(Color.White);
            for (int y = 0; y < n; y++) for (int x = 0; x < n; x++)
                if (q[y, x]) g.FillRectangle(Brushes.Black, (x + 4) * px, (y + 4) * px, px, px);
        }
        return bmp;
    }
}
