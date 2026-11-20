#!/bin/bash
# poner_clave.sh: gives the NVR password to the Android app (debug build) from the Mac's
# Keychain (service camaras-nvr, the same the Mac app uses), over adb. Nothing is printed.
ADB=${ADB:-/usr/local/share/android-commandlinetools/platform-tools/adb}
P=$(security find-generic-password -a admin -s camaras-nvr -w) || { echo "no hay clave en el Llavero"; exit 1; }
Q=${P//\'/\'\\\'\'}   # quote for the phone's shell
"$ADB" shell "am force-stop ar.camaras.app; am start -n ar.camaras.app/.MainActivity --es clave '$Q'" >/dev/null && echo "clave puesta, app abierta"
