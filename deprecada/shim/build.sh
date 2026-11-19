#!/bin/sh
# Cross-builds the P2PDll.dll shim for Windows x64 on macOS.
# Needs clang (Xcode) and lld-link (for example from the Android NDK or Homebrew llvm).
set -e
cd "$(dirname "$0")"
CLANG="${CLANG:-clang}"
LLD="${LLD:-lld-link}"
"$LLD" /lib /def:kernel32.def /out:kernel32.lib /machine:x64
"$CLANG" --target=x86_64-pc-windows-msvc -c shim.c -o shim.obj -O1 -ffreestanding
"$LLD" /dll /entry:DllMain /def:shim.def /out:P2PDll.dll shim.obj kernel32.lib /machine:x64 /nodefaultlib
echo "built shim/P2PDll.dll"
