#!/bin/bash
# Builds ServerMonitor.app (and optionally a .dmg) from the SwiftPM package.
#
# SwiftPM cannot emit an .app bundle, and `xcodebuild` needs full Xcode, so the
# bundle is assembled here by hand. That keeps the whole build reproducible with
# only the Command Line Tools installed.
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
APP_NAME="Server Monitor"
BUNDLE_ID="com.hmchxd.ServerMonitor"
VERSION="${VERSION:-0.1.0}"
CONFIG="${CONFIG:-release}"
DIST="$ROOT/dist"
APP="$DIST/$APP_NAME.app"

echo "==> Building ($CONFIG)"
swift build -c "$CONFIG" --product ServerMonitor

BINARY="$(swift build -c "$CONFIG" --product ServerMonitor --show-bin-path)/ServerMonitor"
[ -f "$BINARY" ] || { echo "binary not found at $BINARY" >&2; exit 1; }

echo "==> Assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BINARY" "$APP/Contents/MacOS/ServerMonitor"

echo "==> Rendering icon"
# No Xcode means no actool, so the .icns is built straight from the SVG with
# headless Chrome (rasterise) plus iconutil (pack).
ICONSET="$DIST/AppIcon.iconset"
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
if [ -x "$CHROME" ]; then
  rm -rf "$ICONSET" && mkdir -p "$ICONSET"
  TMP_DIR="$(mktemp -d)"
  cp "$ROOT/Resources/AppIcon.svg" "$TMP_DIR/icon.svg"
  cat > "$TMP_DIR/icon.html" <<'HTML'
<!doctype html><meta charset="utf-8">
<style>html,body{margin:0;padding:0;background:transparent}img{display:block;width:1024px;height:1024px}</style>
<img src="icon.svg">
HTML
  "$CHROME" --headless=new --disable-gpu --hide-scrollbars \
      --default-background-color=00000000 --window-size=1024,1024 \
      --screenshot="$TMP_DIR/icon-1024.png" "file://$TMP_DIR/icon.html" >/dev/null 2>&1
  for size in 16 32 64 128 256 512; do
    sips -z "$size" "$size" "$TMP_DIR/icon-1024.png" --out "$ICONSET/icon_${size}x${size}.png" >/dev/null
    double=$((size * 2))
    sips -z "$double" "$double" "$TMP_DIR/icon-1024.png" \
        --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"
  rm -rf "$ICONSET" "$TMP_DIR"
else
  echo "    Chrome not found; shipping without an icon"
fi

echo "==> Writing Info.plist"
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>$APP_NAME</string>
  <key>CFBundleDisplayName</key><string>$APP_NAME</string>
  <key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
  <key>CFBundleExecutable</key><string>ServerMonitor</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>LSMinimumSystemVersion</key><string>15.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <!-- A monitoring console with no document model: it should not appear in the
       "Open Recent" style UI and has nothing to reopen. -->
  <key>NSSupportsAutomaticTermination</key><false/>
  <key>NSSupportsSuddenTermination</key><false/>
  <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
</dict>
</plist>
PLIST

echo "==> Signing (ad-hoc)"
# Ad-hoc signing is enough to run locally and keeps the Keychain ACL stable
# within a build. Distribution needs a Developer ID plus notarisation.
codesign --force --deep --sign - "$APP" >/dev/null 2>&1 \
  || echo "    codesign failed; the app will still run locally"

if [ "${MAKE_DMG:-0}" = "1" ]; then
  echo "==> Building dmg"
  DMG="$DIST/$APP_NAME-$VERSION.dmg"
  rm -f "$DMG"
  STAGE="$(mktemp -d)"
  cp -R "$APP" "$STAGE/"
  ln -s /Applications "$STAGE/Applications"
  hdiutil create -volname "$APP_NAME" -srcfolder "$STAGE" -ov -format UDZO "$DMG" >/dev/null
  rm -rf "$STAGE"
  echo "    $DMG"
fi

echo "==> Done: $APP"
