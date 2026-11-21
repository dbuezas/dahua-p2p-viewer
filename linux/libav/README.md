# libav build (ffmpeg inside the app)

`go build -tags libav ./app` decodes with FFmpeg's libraries in the app's own process
(`video/decode_libav.go`) instead of one ffmpeg process per camera. It needs cgo and
FFmpeg 6.1 (Ubuntu 24.04: libavcodec60, libavformat60, libavutil58, libswscale7).

On Ubuntu 24.04 itself (how I built it, on the TV PC):

```sh
apt-get install -y gcc pkg-config libavformat-dev libavcodec-dev libavutil-dev libswscale-dev \
  libgl1-mesa-dev libx11-dev libxcursor-dev libxi-dev libxinerama-dev libxrandr-dev libxxf86vm-dev libasound2-dev
CGO_ENABLED=1 go build -trimpath -tags libav -o camaras-ui ./app
```

From another machine: `linux/libav/build.sh` does the same in Docker.

Measured on the TV PC (i7 Q720; panorama in HD + 2 cameras low, 4 rounds of 5 min):
1.54 -> 1.31 cores (-15%), same frames per second and cuts.
