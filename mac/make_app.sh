#!/bin/bash
# make_app.sh: builds the Mac app as Camaras.app (icon, no terminal window) and puts it
# in ~/Applications. Settings stay in ~/Library/Application Support/camaras-go,
# messages go to ~/Library/Logs/Camaras.log. Needs ffmpeg (Homebrew).
set -e
R="$(cd "$(dirname "$0")/.." && pwd)"
A="$HOME/Applications/Camaras.app"
mkdir -p "$A/Contents/MacOS" "$A/Contents/Resources"
(cd "$R" && CC=/usr/bin/clang go build -o "$A/Contents/MacOS/camaras" ./app)
cp "$R/app/icon/Camaras.icns" "$A/Contents/Resources/Camaras.icns"
cat > "$A/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>Camaras</string>
  <key>CFBundleDisplayName</key><string>Camaras</string>
  <key>CFBundleIdentifier</key><string>local.camaras</string>
  <key>CFBundleExecutable</key><string>camaras</string>
  <key>CFBundleIconFile</key><string>Camaras</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
</dict></plist>
PLIST
touch "$A"
echo "listo: $A"
