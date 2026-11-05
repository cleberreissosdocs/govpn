#!/bin/bash
# install-client-gui-macos.sh — installs govpn-client-gui.app into
# /Applications and sets it to start at login via a LaunchAgent (same
# idea as the Windows installer's Scheduled Task, adapted to macOS: it
# runs as your own user, in your own GUI session — NOT elevated. The
# app itself asks for an admin password only when it actually needs to
# create the network interface. See client-gui/main_darwin.go.
#
# Usage: run from the extracted zip's folder (where govpn-client-gui.app lives)
#   ./install-client-gui-macos.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_SRC="$SCRIPT_DIR/govpn-client-gui.app"
APP_DST="/Applications/govpn-client-gui.app"
PLIST_SRC="$SCRIPT_DIR/com.cleberreissosdocs.govpn.client-gui.plist"
PLIST_DST="$HOME/Library/LaunchAgents/com.cleberreissosdocs.govpn.client-gui.plist"

if [ ! -d "$APP_SRC" ]; then
	echo "Não encontrei $APP_SRC — rode este script de dentro da pasta onde extraiu o .zip." >&2
	exit 1
fi

echo "==> Copiando para /Applications (pode pedir sua senha)"
sudo rm -rf "$APP_DST"
sudo cp -R "$APP_SRC" "$APP_DST"
sudo xattr -dr com.apple.quarantine "$APP_DST" 2>/dev/null || true

echo "==> Instalando o início automático no login (LaunchAgent, roda como você, não como root)"
mkdir -p "$HOME/Library/LaunchAgents"
cp "$PLIST_SRC" "$PLIST_DST"
launchctl unload "$PLIST_DST" 2>/dev/null || true
launchctl load "$PLIST_DST"

echo "==> Pronto. Ícone deve aparecer na barra de menu em alguns segundos."
echo "    Primeira vez: clique com o botão direito no ícone -> \"Abrir configuração\""
echo "    e preencha server/authkey. Ao conectar, o macOS vai pedir sua senha de"
echo "    administrador uma vez (é o passo que cria a interface de rede)."
