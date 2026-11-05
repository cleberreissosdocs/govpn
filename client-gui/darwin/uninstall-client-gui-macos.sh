#!/bin/bash
# uninstall-client-gui-macos.sh — stops and removes govpn-client-gui,
# including the root LaunchDaemon set up at install time.
set -euo pipefail

DAEMON_PLIST="/Library/LaunchDaemons/com.cleberreissosdocs.govpn.client-daemon.plist"
AGENT_PLIST="$HOME/Library/LaunchAgents/com.cleberreissosdocs.govpn.client-gui.plist"
APP="/Applications/govpn-client-gui.app"
SHARED_DIR="/Library/Application Support/govpn"

echo "==> Parando e removendo o serviço (pede sua senha — é root)"
sudo launchctl bootout system "$DAEMON_PLIST" 2>/dev/null || sudo launchctl unload "$DAEMON_PLIST" 2>/dev/null || true
sudo rm -f "$DAEMON_PLIST"

echo "==> Removendo o ícone da bandeja"
launchctl unload "$AGENT_PLIST" 2>/dev/null || true
rm -f "$AGENT_PLIST"

echo "==> Removendo o app"
sudo rm -rf "$APP"

read -r -p "Apagar também config/estado salvo (identidade do dispositivo na malha)? [s/N] " resp
if [[ "$resp" =~ ^[sS]$ ]]; then
	sudo rm -rf "$SHARED_DIR" "$HOME/.govpn"
	echo "Removido."
else
	echo "Config/estado mantidos em $SHARED_DIR"
fi
