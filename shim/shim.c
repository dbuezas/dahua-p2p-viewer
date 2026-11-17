// P2PDll.dll shim: logs what SmartPSS passes to P2P_Init and P2P_Connect, then calls the real DLL (P2Preal.dll).
// The remaining exports are PE forwarders (in shim.def). Freestanding: no CRT, only kernel32 by name.
typedef unsigned long long u64;
typedef unsigned int u32;
typedef unsigned char u8;
typedef void *PV;

__declspec(dllimport) PV __stdcall LoadLibraryA(const char *);
__declspec(dllimport) PV __stdcall GetProcAddress(PV, const char *);
__declspec(dllimport) PV __stdcall CreateFileA(const char *, u32, u32, PV, u32, u32, PV);
__declspec(dllimport) int __stdcall WriteFile(PV, const void *, u32, u32 *, PV);
__declspec(dllimport) int __stdcall CloseHandle(PV);
__declspec(dllimport) u32 __stdcall SetFilePointer(PV, long, PV, u32);
__declspec(dllimport) int __stdcall IsBadReadPtr(const void *, u64);

#define GENERIC_WRITE 0x40000000u
#define FILE_SHARE_RW 0x3u
#define OPEN_ALWAYS 4u
#define FILE_END 2u

static const char *LOG = "C:\\Users\\Public\\camapp\\shim.log";

static u64 slen(const char *s) { u64 n = 0; while (s && s[n]) n++; return n; }

static void append(const char *buf, u32 n) {
    PV h = CreateFileA(LOG, GENERIC_WRITE, FILE_SHARE_RW, 0, OPEN_ALWAYS, 0x80, 0);
    if (!h || h == (PV)-1) return;
    SetFilePointer(h, 0, 0, FILE_END);
    u32 w;
    WriteFile(h, buf, n, &w, 0);
    CloseHandle(h);
}

static void puts_(const char *s) { append(s, (u32)slen(s)); }

static void phex(u64 v, int bytes) {
    char b[19]; int i = 0; b[i++] = '0'; b[i++] = 'x';
    for (int sh = (bytes * 8) - 4; sh >= 0; sh -= 4) {
        int d = (int)((v >> sh) & 0xf);
        b[i++] = d < 10 ? '0' + d : 'a' + d - 10;
    }
    append(b, i);
}

static void dump(const char *label, const void *p, u32 n) {
    puts_("  "); puts_(label); puts_(" @"); phex((u64)p, 8); puts_(":\n");
    if (!p || IsBadReadPtr(p, n)) { puts_("    (no leible)\n"); return; }
    const u8 *b = (const u8 *)p;
    char line[128];
    for (u32 off = 0; off < n; off += 16) {
        int i = 0;
        line[i++] = ' '; line[i++] = ' ';
        for (u32 k = 0; k < 16 && off + k < n; k++) {
            u8 c = b[off + k];
            line[i++] = "0123456789abcdef"[c >> 4];
            line[i++] = "0123456789abcdef"[c & 0xf];
            line[i++] = ' ';
        }
        line[i++] = '|';
        for (u32 k = 0; k < 16 && off + k < n; k++) {
            u8 c = b[off + k];
            line[i++] = (c >= 32 && c < 127) ? c : '.';
        }
        line[i++] = '\n';
        append(line, i);
    }
}

// If v looks like a readable pointer to a short string, print it; otherwise just hex.
static void arg(const char *name, u64 v) {
    puts_("  "); puts_(name); puts_("="); phex(v, 8);
    if (v > 0x10000 && !IsBadReadPtr((PV)v, 1)) {
        const char *s = (const char *)v;
        u64 n = slen(s);
        if (n > 0 && n < 80) {
            int ok = 1;
            for (u64 k = 0; k < n; k++) { u8 c = ((const u8 *)s)[k]; if (c < 32 || c > 126) { ok = 0; break; } }
            if (ok) { puts_(" str=\""); append(s, (u32)n); puts_("\""); }
        }
    }
    puts_("\n");
}

typedef u64(__stdcall *pfn7)(PV, PV, u64, PV, u64, u64, u64);
typedef u64(__stdcall *pfn9)(u64, u64, u64, u64, u64, u64, u64, u64, u64);

static PV real(const char *name) { return GetProcAddress(LoadLibraryA("P2Preal.dll"), name); }

__declspec(dllexport) u64 __stdcall P2P_Init(PV a1, PV a2, u64 a3, PV a4, u64 a5, u64 a6, u64 a7) {
    puts_("== P2P_Init ==\n");
    arg("a1(rcx)", (u64)a1); arg("a2(rdx)", (u64)a2); arg("a3(r8)", a3); arg("a4(r9)", (u64)a4);
    arg("a5", a5); arg("a6", a6); arg("a7", a7);
    dump("a5 ptr", (PV)a5, 48);
    dump("a6 ptr", (PV)a6, 48);
    pfn7 r = (pfn7)real("P2P_Init");
    if (!r) { puts_("  ERROR real\n"); return 1; }
    u64 ret = r(a1, a2, a3, a4, a5, a6, a7);
    puts_("  -> ret="); phex(ret, 8); puts_("\n");
    return ret;
}

__declspec(dllexport) u64 __stdcall P2P_Connect(u64 a1, u64 a2, u64 a3, u64 a4, u64 a5, u64 a6, u64 a7, u64 a8, u64 a9) {
    puts_("== P2P_Connect ==\n");
    arg("a1(rcx)", a1); arg("a2(rdx)", a2); arg("a3(r8)", a3); arg("a4(r9)", a4);
    arg("a5", a5); arg("a6", a6); arg("a7", a7); arg("a8", a8); arg("a9", a9);
    if (a5 > 0x10000 && !IsBadReadPtr((PV)a5, 4)) dump("a5 ptr(localPort?)", (PV)a5, 16);
    pfn9 r = (pfn9)real("P2P_Connect");
    if (!r) { puts_("  ERROR real\n"); return 1; }
    u64 ret = r(a1, a2, a3, a4, a5, a6, a7, a8, a9);
    puts_("  -> ret="); phex(ret, 8);
    if (a5 > 0x10000 && !IsBadReadPtr((PV)a5, 4)) { puts_(" localPort="); phex(*(u32 *)a5, 4); }
    puts_("\n");
    return ret;
}

__declspec(dllexport) u64 __stdcall P2P_GetDeviceInfo(u64 a1, u64 a2, u64 a3, u64 a4, u64 a5, u64 a6, u64 a7, u64 a8) {
    puts_("== P2P_GetDeviceInfo ==\n");
    arg("a1", a1); arg("a2", a2); arg("a3", a3); arg("a4", a4);
    arg("a5", a5); arg("a6", a6); arg("a7", a7); arg("a8", a8);
    pfn9 r = (pfn9)real("P2P_GetDeviceInfo");
    if (!r) { puts_("  ERROR real\n"); return 1; }
    u64 ret = r(a1, a2, a3, a4, a5, a6, a7, a8, 0);
    puts_("  -> ret="); phex(ret, 8); puts_("\n");
    dump("a2 despues", (PV)a2, 64);
    return ret;
}

int __stdcall DllMain(PV h, u32 reason, PV r) { (void)h; (void)reason; (void)r; return 1; }
