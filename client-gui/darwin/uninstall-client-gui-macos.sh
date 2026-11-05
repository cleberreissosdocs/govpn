#!/bin/bash
# uninstall-client-gui-macos.sh — stops and removes govpn-client-gui.
set -euo pipefail

PLIST="$HOME/Library/LaunchAgents/com.cleberreissosdocs.govpn.client-gui.plist"
APP="/Applications/govpn-client-gui.app"
STATE_DIR="$HOME/.govpn"

echo "==> Parando o túnel, se estiver rodando (pode pedir sua senha)"
if [ -f "$STATE_DIR/gui_child.pid" ]; then
	sudo kill "$(cat "$STATE_DIR/gui_child.pid")" 2>/dev/null || true
	rm -f "$STATE_DIR/gui_child.pid"
fi

echo "==> Removendo início automático"
launchctl unload "$PLIST" 2>/dev/null || true
rm -f "$PLIST"

echo "==> Removendo o app"
sudo rm -rf "$APP"

read -r -p "Apagar também config/estado salvo (identidade do dispositivo na malha)? [s/N] " resp
if [[ "$resp" =~ ^[sS]$ ]]; then
	rm -rf "$STATE_DIR" "$HOME/Library/Application Support/govpn"
	echo "Removido."
else
	echo "Config/estado mantidos em $STATE_DIR e ~/Library/Application Support/govpn"
fi
