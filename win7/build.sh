#!/bin/bash
# Builds camaras-ui.exe for Windows 7 (Go 1.20 is the last Go that runs there,
# with Ebiten 2.7). The go.mod files here pin those versions; the sources are
# copied from the main tree into a fresh build folder.
# Needs: go install golang.org/dl/go1.20.14@latest && go1.20.14 download
set -e -o pipefail
R="$(cd "$(dirname "$0")/.." && pwd)"; W="${TMPDIR:-/tmp}/camaras-win7"
rm -rf "$W"; mkdir -p "$W"; cp "$R/win7/go.work" "$W/"
for m in tunnel nvr nvrlib calib video app web; do
  mkdir -p "$W/$m"; cp "$R/win7/$m/"* "$W/$m/"
  (cd "$R/$m" && find . -maxdepth 1 -type f \( -name '*.go' -o -name '*.html' -o -name '*.js' \) ! -name '*_test.go' -exec cp {} "$W/$m/" \;)
  [ -d "$R/$m/icon" ] && cp -r "$R/$m/icon" "$W/$m/"
done
cd "$W/app"; GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go1.20.14 build -ldflags "-H=windowsgui" -o "$R/camaras-ui.exe" .
echo "listo: $R/camaras-ui.exe"
