# dahua-p2p-viewer

Shows all cameras of a Dahua NVR full screen, through Dahua's P2P cloud, with **no Dahua DLLs**.
It runs on Windows (7 and later), macOS and Android.

I wrote it to replace my old Windows app, which needed the NetSDK DLLs from SmartPSS Lite.
The old app is kept in [`deprecada/`](deprecada/).

## What it does

- **Own P2P tunnel** to the NVR by its serial number (`tunnel/`). It speaks the same protocol as
  Dahua's `P2PDll.dll`: the cloud handshake, a direct UDP path when the NVR answers (relay
  otherwise), and the PTCP byte stream (libjingle PseudoTcp, with NACKs, delayed ACKs and
  retransmission). A stalled session is replaced on its own.
- **NVR RPC** (`nvrlib/`): login, camera list, snapshots, PTZ. The PTZ joystick copies the old
  app's logic (ordered command queue, speed by drag distance, pulses below speed 1).
- **Video**: one ffmpeg per camera (RTSP over the tunnel) gives YUV frames; the graphics card
  converts them. No console windows on Windows.
- **Panorama** of a dual-lens camera (`calib/`): calibrate once (also from a phone, with
  `web/`), then it is drawn on the graphics card each frame.
- **App** (`app/`, Ebiten): grid of cameras, click to enlarge, smooth digital zoom, HD per
  camera, PTZ by dragging, remote control page for a phone.
- **Android** (`android/` + `mobile/`): the same Go tunnel and NVR code as a library, video by
  ExoPlayer (hardware decoder, software fallback), the panorama in GLSL, portrait and
  landscape.

## Layout

| Folder | What |
|---|---|
| `tunnel/` | P2P tunnel library (`p2ptunnel`) and the `dh-p2p` command |
| `nvrlib/` | NVR login, cameras, snapshot, PTZ |
| `video/` | ffmpeg decoder |
| `calib/` | panorama calibration and mesh |
| `web/` | remote control and calibration pages, WebSocket server |
| `compartir/` | the configuration link for the QR code (see below) |
| `mostrar/` | prints the settings and their QR codes in a terminal |
| `app/` | desktop app |
| `mobile/` | Go core for Android (`gomobile bind`) |
| `android/` | Android app (Java) |
| `mac/` | builds `Camaras.app` |
| `win7/` | Windows 7 build (Go 1.20, Ebiten 2.7) |
| `deprecada/` | the old DLL app |

## Setup

- **NVR serial**: put it in `sn.txt` in the settings folder (or set `sn=` in `camaras.ini`).
- **NVR password**: the app asks for it on screen the first time and stores it in the Keychain
  (macOS) or with DPAPI (Windows).
- **Settings folder**: next to the exe if a `camaras.ini` is there, else the user's config folder
  (`~/Library/Application Support/camaras-go` on macOS). The files are the same as the old app's:
  `camaras.ini`, `hd.txt`, `orden.txt`, `panoramica.txt`.
- **ffmpeg** must be on the PATH (on Windows, next to the exe works).

`camaras.ini` keys (all optional): `sn`, `ptz` (PTZ camera channels), `ptz_velocidad`,
`ptz_invertir_pan`, `dividir` (dual-lens camera channel), `colchon_ms` (playout cushion),
`puerto_web` (remote control port), `servidores` (cloud servers).

## Copy the configuration to a phone

One QR code carries the whole configuration except the panorama calibration: serial, NVR
user and password, the `camaras.ini` keys and the `hd`/`orden`/`panoramica` lists. It is a link

```
camaras://config?v=1&d=<base64url, no padding, of a small JSON>
```

(`compartir/` makes and reads it; `v` is the format version). Scanned with an Android phone's
camera, it opens the app, which shows what it holds (password hidden) and asks
**Importar** / **Cancelar**. The QR holds the password, so it only shows on request:

- **TV / desktop app**: key **C**, or the button at the bottom of the phone remote page. It
  hides by itself after 60 s (C or Esc sooner).
- **Android app**: long press outside the cameras -> Ajustes -> **Compartir configuración**
  (full screen until Cerrar; no screenshots while it shows).
- **Terminal**: `cd mostrar && go run .` prints it too ("QR para importar en la app").

## Build

**macOS**

```sh
mac/make_app.sh          # builds ~/Applications/Camaras.app (needs Homebrew ffmpeg)
```

**Windows 10/11**

```sh
cd app && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui" -o camaras-ui.exe .
```

**Windows 7** (Go 1.20 is the last Go that runs there)

```sh
go install golang.org/dl/go1.20.14@latest && go1.20.14 download
win7/build.sh            # gives camaras-ui.exe
```

**Android** (needs the Android SDK, NDK and `gomobile`)

```sh
cd mobile && gomobile bind -target=android/arm64,android/amd64 -androidapi 24 -javapkg ar.camaras -o ../android/camaras.aar .
cd ../android && ./gradlew assembleDebug
```

The app asks for the serial and password in its settings. For a debug build,
`android/poner_clave.sh` copies the password from the Mac's Keychain to the phone over adb.

## Releases

Ready-made builds for all platforms are on the
[Releases](https://github.com/dbuezas/dahua-p2p-viewer/releases) page. They are built by
`.github/workflows/release.yml` when I push a tag like `v1.2.3` (a manual run of the workflow
only keeps them as workflow artifacts). ffmpeg is not included: install it separately for the
desktop builds. The APK is debug-signed.

## Credits

The first version of the tunnel was a port of
[khoanguyen-3fc/dh-p2p](https://github.com/khoanguyen-3fc/dh-p2p), which worked out the Dahua P2P
handshake. The rest of the protocol (direct path, PTCP details) comes from the Dahua DLLs.

## Linux on old NVIDIA GPUs (nouveau, GeForce 8/9/200/300)

Mesa's nouveau driver made a new GPU buffer for every fence (every flush); on NV50-family GPUs
each one costs a TLB flush that busy-waits for the GPU, about 30% of the app's CPU on a GT 230M.
`linux/mesa/` has a small patch (recycle the fence buffers) and `build.sh`, which builds the
distro's Mesa with it into `/opt/mesa-nv50` for this app only.
