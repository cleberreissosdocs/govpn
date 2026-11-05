#!/bin/bash
# install-client-gui-macos.sh — manual/Terminal alternative to the
# .pkg installer (same end result: a root LaunchDaemon running the VPN
# forever from boot, no password asked ever again, plus an unprivileged
# per-user tray icon — see client-gui/main_darwin.go).
#
# Usage: run from the extracted zip's folder (where govpn-client-gui.app lives)
#   ./install-client-gui-macos.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_SRC="$SCRIPT_DIR/govpn-client-gui.app"
APP_DST="/Applications/govpn-client-gui.app"

if [ ! -d "$APP_SRC" ]; then
	echo "Não encontrei $APP_SRC — rode este script de dentro da pasta onde extraiu o .zip." >&2
	exit 1
fi

echo "==> Copiando para /Applications e configurando o serviço (vai pedir sua senha — só agora, nunca mais)"
sudo rm -rf "$APP_DST"
sudo cp -R "$APP_SRC" "$APP_DST"
sudo bash "$SCRIPT_DIR/postinstall"

echo "==> Pronto. Ícone deve aparecer na barra de menu em alguns segundos."
echo "    Clique com o botão direito -> \"Abrir configuração\" e preencha server/authkey."
echo "    O serviço (já rodando como root desde agora) conecta sozinho, sem pedir senha de novo."
