#!/bin/bash
# Builds the distro's Mesa with nouveau-recycle-fence-bo.patch into /opt/mesa-nv50, for the
# camera app only (Ubuntu 24.04, run as root on the Linux PC; ~10 min).
#
# Why: on NVIDIA NV50-family GPUs (GeForce 8/9/200/300, e.g. GT 230M) Mesa's nouveau driver made
# a new 4 KiB buffer for every fence (every flush). Each new buffer is mapped into the GPU VM and
# each freed one unmapped, and on these GPUs every map/unmap is a TLB flush that busy-waits for
# the graphics engine. That was ~30% of the camera app's CPU; with the patch it is ~0.5%.
#
# Use: put  export LD_LIBRARY_PATH=/opt/mesa-nv50/lib/x86_64-linux-gnu  in ~/camaras/mesa.env
# (the launcher sources it); delete that file to go back to the system Mesa.
set -euo pipefail
V=$(dpkg-query -W -f='${Version}' mesa-libgallium)
sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/ubuntu.sources
apt-get update -q
DEBIAN_FRONTEND=noninteractive apt-get build-dep -y -q mesa
mkdir -p /opt/src && cd /opt/src && apt-get source -q "mesa=$V"
cd /opt/src/mesa-*/
patch -p1 < "$(dirname "$(readlink -f "$0")")/nouveau-recycle-fence-bo.patch"
meson setup build-nv50 --wipe -Dprefix=/opt/mesa-nv50 -Dbuildtype=release \
  -Dgallium-drivers=nouveau -Dvulkan-drivers= -Dllvm=disabled -Dplatforms=x11 \
  -Dglx=dri -Degl=enabled -Dgbm=enabled -Dglvnd=enabled -Dgles1=disabled -Dgles2=enabled \
  -Dvideo-codecs= -Dgallium-va=disabled -Dgallium-vdpau=disabled -Dtools= -Dvalgrind=disabled \
  -Dlibunwind=disabled -Dlmsensors=disabled -Dbuild-tests=false
ninja -C build-nv50
ninja -C build-nv50 install
echo "listo: /opt/mesa-nv50"
