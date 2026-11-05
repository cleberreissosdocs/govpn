#!/bin/bash
# build-app.sh — assembles the govpn-client-gui.app bundle for macOS.
# Must run on a real Mac (or a macOS GitHub Actions runner): the tray
# icon uses cgo + Cocoa, which cannot be cross-compiled from Linux.
#
# Usage (from the repo root):
#   ./client-gui/darwin/build-app.sh
#
# Produces dist/govpn-client-gui.app and dist/govpn-client-gui-macos.zip
# (universal binary: arm64 + Intel, both the GUI and the CLI it
# launches elevated are bundled side by side in Contents/MacOS/).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

APPDIR="dist/govpn-client-gui.app"
rm -rf dist
mkdir -p "$APPDIR/Contents/MacOS" "$APPDIR/Contents/Resources"

echo "==> Building govpn-client-gui (arm64 + amd64)"
export CGO_ENABLED=1   # required: the tray icon (getlantern/systray) uses cgo+Cocoa on darwin
GOOS=darwin GOARCH=arm64 go build -o /tmp/govpn-gui-arm64 ./client-gui/
GOOS=darwin GOARCH=amd64 go build -o /tmp/govpn-gui-amd64 ./client-gui/
lipo -create -output "$APPDIR/Contents/MacOS/govpn-client-gui" /tmp/govpn-gui-arm64 /tmp/govpn-gui-amd64

echo "==> Building govpn-client CLI (arm64 + amd64) — bundled alongside, launched elevated on demand"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/govpn-cli-arm64 ./client/
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /tmp/govpn-cli-amd64 ./client/
lipo -create -output "$APPDIR/Contents/MacOS/govpn-client" /tmp/govpn-cli-arm64 /tmp/govpn-cli-amd64

chmod +x "$APPDIR/Contents/MacOS/"*
cp client-gui/darwin/Info.plist "$APPDIR/Contents/Info.plist"

echo "==> Bundling install/uninstall scripts + LaunchAgent plist alongside the .app"
cp client-gui/darwin/com.cleberreissosdocs.govpn.client-gui.plist dist/
cp client-gui/darwin/install-client-gui-macos.sh dist/
cp client-gui/darwin/uninstall-client-gui-macos.sh dist/
chmod +x dist/install-client-gui-macos.sh dist/uninstall-client-gui-macos.sh

echo "==> Zipping"
( cd dist && zip -qr govpn-client-gui-macos.zip govpn-client-gui.app com.cleberreissosdocs.govpn.client-gui.plist install-client-gui-macos.sh uninstall-client-gui-macos.sh )

echo "==> Done: dist/govpn-client-gui-macos.zip"
file "$APPDIR/Contents/MacOS/govpn-client-gui"
