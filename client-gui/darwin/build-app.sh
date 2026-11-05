#!/bin/bash
# build-app.sh — assembles the govpn-client-gui.app bundle for macOS.
# Must run on a real Mac (or a macOS GitHub Actions runner): the tray
# icon uses cgo + Cocoa, which cannot be cross-compiled from Linux.
#
# Usage (from the repo root):
#   ./client-gui/darwin/build-app.sh
#
# Produces dist/govpn-client-gui.pkg (double-click installer) and
# dist/govpn-client-gui-macos.zip (manual/Terminal alternative) —
# universal binaries: arm64 + Intel, both the GUI and the CLI it uses
# as a root daemon are bundled side by side in Contents/MacOS/.

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

echo "==> Building govpn-client CLI (arm64 + amd64) — this is what runs as the root LaunchDaemon"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/govpn-cli-arm64 ./client/
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /tmp/govpn-cli-amd64 ./client/
lipo -create -output "$APPDIR/Contents/MacOS/govpn-client" /tmp/govpn-cli-arm64 /tmp/govpn-cli-amd64

chmod +x "$APPDIR/Contents/MacOS/"*
cp client-gui/darwin/Info.plist "$APPDIR/Contents/Info.plist"

echo "==> Bundling install/uninstall scripts + postinstall alongside the .app (manual/zip route)"
cp client-gui/darwin/pkg-scripts/postinstall dist/
cp client-gui/darwin/install-client-gui-macos.sh dist/
cp client-gui/darwin/uninstall-client-gui-macos.sh dist/
chmod +x dist/postinstall dist/install-client-gui-macos.sh dist/uninstall-client-gui-macos.sh

echo "==> Building .pkg installer (double-click wizard, like govpn-setup.exe on Windows)"
PKGROOT=/tmp/govpn-pkgroot
rm -rf "$PKGROOT"
mkdir -p "$PKGROOT/Applications"
cp -R "$APPDIR" "$PKGROOT/Applications/"
chmod +x client-gui/darwin/pkg-scripts/postinstall
pkgbuild \
	--root "$PKGROOT" \
	--scripts client-gui/darwin/pkg-scripts \
	--identifier com.cleberreissosdocs.govpn.client-gui \
	--version 1.0.0 \
	--install-location / \
	dist/govpn-client-gui.pkg

echo "==> Zipping (.app + scripts, for people who prefer the manual/Terminal route)"
( cd dist && zip -qr govpn-client-gui-macos.zip govpn-client-gui.app postinstall install-client-gui-macos.sh uninstall-client-gui-macos.sh )

echo "==> Done: dist/govpn-client-gui-macos.zip and dist/govpn-client-gui.pkg"
file "$APPDIR/Contents/MacOS/govpn-client-gui"
