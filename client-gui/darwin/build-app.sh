#!/bin/bash
# build-app.sh — assembles the govpn-client-gui.app bundle(s) for
# macOS. Must run on a real Mac (or a macOS GitHub Actions runner):
# the tray icon uses cgo + Cocoa, which cannot be cross-compiled from
# Linux.
#
# Usage (from the repo root):
#   ./client-gui/darwin/build-app.sh
#
# Produces TWO independent installers, same features/menu/icon, built
# from the exact same Go source — they only differ in which CPU
# architectures (and therefore which minimum macOS version) they
# support:
#
#   dist/govpn-client-gui.pkg / -macos.zip
#     Universal (arm64 + Intel). For current Macs — Apple Silicon or a
#     reasonably recent Intel one. Requires macOS 11 (Big Sur)+.
#
#   dist/govpn-client-gui-ventura-intel.pkg / -ventura-intel-macos.zip
#     Intel-only (x86_64), no arm64 slice. For older Intel Macs capped
#     at macOS 13 Ventura or earlier (common on ~2013-2015 hardware,
#     stock or via OpenCore Legacy Patcher). Smaller download, same
#     LaunchDaemon/tray behavior — just an older compatibility floor
#     (macOS 10.15, the oldest the current Go toolchain supports for
#     darwin/amd64). Note: the universal build above already WORKS on
#     an Intel Mac running Ventura (13 >= its 11.0 floor) — this
#     variant exists because it was asked for explicitly, as a
#     dedicated/lighter option for older hardware, not because the
#     universal one is broken there.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

rm -rf dist
mkdir -p dist

export CGO_ENABLED=1   # required: the tray icon (getlantern/systray) uses cgo+Cocoa on darwin

echo "==> Building govpn-client-gui (arm64 + amd64)"
GOOS=darwin GOARCH=arm64 go build -o /tmp/govpn-gui-arm64 ./client-gui/
GOOS=darwin GOARCH=amd64 go build -o /tmp/govpn-gui-amd64 ./client-gui/

echo "==> Building govpn-client CLI (arm64 + amd64) — this is what runs as the root LaunchDaemon"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/govpn-cli-arm64 ./client/
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /tmp/govpn-cli-amd64 ./client/

# assemble_app <appdir> <info.plist> <gui-binaries...> -- <cli-binaries...>
assemble_app() {
	local appdir="$1" infoplist="$2"; shift 2
	mkdir -p "$appdir/Contents/MacOS" "$appdir/Contents/Resources"
	local gui_bins=() cli_bins=() mode="gui"
	for a in "$@"; do
		if [ "$a" = "--" ]; then mode="cli"; continue; fi
		if [ "$mode" = "gui" ]; then gui_bins+=("$a"); else cli_bins+=("$a"); fi
	done
	lipo -create -output "$appdir/Contents/MacOS/govpn-client-gui" "${gui_bins[@]}"
	lipo -create -output "$appdir/Contents/MacOS/govpn-client" "${cli_bins[@]}"
	chmod +x "$appdir/Contents/MacOS/"*
	cp "$infoplist" "$appdir/Contents/Info.plist"
}

# build_pkg_and_zip <appdir> <pkg-basename>
build_pkg_and_zip() {
	local appdir="$1" base="$2"
	local pkgroot=/tmp/govpn-pkgroot-$base
	rm -rf "$pkgroot"
	mkdir -p "$pkgroot/Applications"
	cp -R "$appdir" "$pkgroot/Applications/govpn-client-gui.app"
	pkgbuild \
		--root "$pkgroot" \
		--scripts client-gui/darwin/pkg-scripts \
		--identifier com.cleberreissosdocs.govpn.client-gui \
		--version 1.0.0 \
		--install-location / \
		"dist/$base.pkg"

	local zipdir=/tmp/govpn-zip-$base
	rm -rf "$zipdir"
	mkdir -p "$zipdir"
	cp -R "$appdir" "$zipdir/govpn-client-gui.app"
	cp client-gui/darwin/pkg-scripts/postinstall "$zipdir/"
	cp client-gui/darwin/install-client-gui-macos.sh "$zipdir/"
	cp client-gui/darwin/uninstall-client-gui-macos.sh "$zipdir/"
	chmod +x "$zipdir/postinstall" "$zipdir/install-client-gui-macos.sh" "$zipdir/uninstall-client-gui-macos.sh"
	( cd "$zipdir" && zip -qr "$ROOT/dist/$base-macos.zip" . )
}

echo "==> Variant 1/2: universal (arm64 + Intel), macOS 11+"
assemble_app dist/govpn-client-gui.app client-gui/darwin/Info.plist \
	/tmp/govpn-gui-arm64 /tmp/govpn-gui-amd64 -- /tmp/govpn-cli-arm64 /tmp/govpn-cli-amd64
build_pkg_and_zip dist/govpn-client-gui.app govpn-client-gui

echo "==> Variant 2/2: Intel-only, macOS 10.15+ (older Macs capped at Ventura/13 or earlier)"
assemble_app dist/govpn-client-gui-ventura-intel.app client-gui/darwin/Info-ventura-intel.plist \
	/tmp/govpn-gui-amd64 -- /tmp/govpn-cli-amd64
build_pkg_and_zip dist/govpn-client-gui-ventura-intel.app govpn-client-gui-ventura-intel

echo "==> Done:"
ls -la dist/*.pkg dist/*.zip
file dist/govpn-client-gui.app/Contents/MacOS/govpn-client-gui
file dist/govpn-client-gui-ventura-intel.app/Contents/MacOS/govpn-client-gui
