# Builds the Linux app with ffmpeg's libraries inside (-tags libav) on Ubuntu 24.04,
# so it links against the same libav* versions as the TV PC (FFmpeg 6.1).
FROM golang:1.27.3 AS go
FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates gcc pkg-config libgl1-mesa-dev libx11-dev libxcursor-dev libxi-dev libxinerama-dev \
      libxrandr-dev libxxf86vm-dev libasound2-dev \
      libavformat-dev libavcodec-dev libavutil-dev libswscale-dev \
    && apt-get clean
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local CGO_ENABLED=1
WORKDIR /src
