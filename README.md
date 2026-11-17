# dahua-p2p-viewer

A tiny Windows app that shows all cameras of a Dahua NVR full screen, nothing else.
It connects through Dahua's P2P cloud (by serial number), so the NVR needs no open ports.

I made it for an old Windows 7 PC that shows the cameras of a house far away.
SmartPSS Lite worked, but it took about a minute to show video (it asks every one of the 32 channels
for its settings, and waits 5 s on each empty one) and it needs clicks every time.
This app shows the cameras in about 10 seconds.

- Finds the channels that have a camera by itself (and saves the list, so the next start is fast).
  A camera added to the NVR later shows up on the next start.
- A channel whose picture is two cameras stacked can be split into two squares (`dividir` in `camaras.ini`).
- Click a camera: it gets big, and the others stay small (10% of the screen width) on the side or below.
  Click a small one to make it big instead. Click the big one or Esc: all cameras. Esc with all cameras: close.
- Right click a camera: switch it between low quality (sub stream, fast) and HD (main stream). A red "HD"
  shows on HD cameras. The choice is saved in `hd.txt`. Too many HD cameras at once can hit the NVR's stream limit.
- Drag a camera onto another one: they swap places. The order is saved in `orden.txt`.
- Only one copy runs. Opening it again brings the running copy to the front.
- Uses as much of the screen as possible without stretching the pictures: it tries normal grids
  and rows with different counts (for example 5 cameras = 2 big on top + 3 smaller below) and keeps the bigger one.
- Watchdog: a camera that stops sending video for 20 s is reopened. If no camera sends video for 90 s,
  or the PC wakes up from sleep, the app starts again from zero (new P2P tunnel and login).

## How it works

It does not talk to the Dahua cloud by itself. It loads two DLLs that SmartPSS Lite installs:

1. `P2PDll.dll` opens a local TCP tunnel to the NVR by its serial number
   (`P2P_Init` → `P2P_DeviceStatus` → `P2P_Connect` → `P2P_QueryChannelStatus`).
2. `dhnetsdk.dll` (Dahua NetSDK) logs in to `127.0.0.1:<tunnel port>` and plays the live video into windows.

These functions have no public documentation. I found the arguments with the shim in `shim/`:
a fake `P2PDll.dll` that writes every argument SmartPSS passes to a log file, then calls the real DLL.

```
P2P_Init(0, serverIp, 8800, key, user, &handle)   // key and user: two strings per server
P2P_Connect(0, handle, serial, 37777, &localPort, nvrUser, nvrPassword, "", "")
```

Two things that took me a while:

- `P2P_Init` takes **two** strings per server (`key` and `user`). With only one, the server never answers.
- The P2P library cannot resolve host names on Windows 7, so the app resolves them first and passes the IP.

## Setup

1. Install SmartPSS Lite (for its DLLs; it does not need to run).
2. Copy `camaras.ini.example` to `camaras.ini` and fill in the serial number of the NVR.
3. Capture `key` and `user` for each P2P server with the shim (below) and put them in `servidores`.
4. Build: run `build.ps1` on the Windows PC (uses `csc.exe` from .NET Framework 4, no Visual Studio needed).
5. Start `Camaras.exe`. The first time it asks for the NVR password and saves it encrypted (Windows DPAPI).

## Capturing the P2P strings with the shim

1. Build the shim on macOS: `shim/build.sh` (needs clang and `lld-link`, for example from the Android NDK:
   `LLD=$ANDROID_HOME/ndk/<version>/toolchains/llvm/prebuilt/darwin-x86_64/bin/lld-link shim/build.sh`).
2. On the Windows PC, in the SmartPSS Lite folder: rename `P2PDll.dll` to `P2Preal.dll`
   and copy the shim's `P2PDll.dll` there.
3. Start SmartPSS Lite and open the cameras. The arguments land in `C:\Users\Public\camapp\shim.log`.
   `P2P_Init` shows `a2` = server IP, `a4` = key, `a5` = user.
4. Undo step 2 (delete the shim, rename `P2Preal.dll` back to `P2PDll.dll`).

The log also contains the NVR password (`P2P_Connect` argument `a7`), so delete it when done.

## Files

- `Camaras.cs`: the app (C# 5, WinForms, .NET Framework 4, x64).
- `camaras.ini.example`: settings template.
- `build.ps1`: builds `Camaras.exe` on Windows.
- `shim/`: the logging `P2PDll.dll` (plain C, no C runtime) and its build script.
