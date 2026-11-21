#!/bin/sh
# build.sh [out]: the Linux app with ffmpeg's libraries inside, built in Docker
# (Ubuntu 24.04 = the TV PC's libraries). Default out: dist/camaras-linux-libav
set -e
cd "$(dirname "$0")/../.."
out=${1:-dist/camaras-linux-libav}
docker build -q -t camaras-libav linux/libav >/dev/null
mkdir -p "$(dirname "$out")"
docker run --rm -v "$PWD":/src -v camaras-libav-gocache:/root/.cache -v camaras-libav-gomod:/root/go/pkg/mod \
  camaras-libav go build -trimpath -tags libav -o "$out" ./app
echo "$out"
