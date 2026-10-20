#!/bin/bash
# uninstall-client.sh — remove o serviço, o binário e opcionalmente a
# configuração/estado do govpn-client instalados pelo install-client.sh.

set -euo pipefail

INSTALL_DIR="/opt/govpn"
CONFIG_DIR="/etc/govpn"
STATE_DIR="/var/lib/govpn"
BIN_NAME="govpn-client"
SERVICE_NAME="govpn-client"

if [ "$(id -u)" -ne 0 ]; then
	echo "Precisa rodar como root (sudo ./uninstall-client.sh)." >&2
	exit 1
fi

echo "==> Parando e desabilitando o serviço"
systemctl stop "${SERVICE_NAME}.service" 2>/dev/null || true
systemctl disable "${SERVICE_NAME}.service" 2>/dev/null || true
rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
systemctl daemon-reload

echo "==> Removendo binário"
rm -f "$INSTALL_DIR/$BIN_NAME"
rmdir "$INSTALL_DIR" 2>/dev/null || true

read -r -p "Remover também a configuração ($CONFIG_DIR) e o estado/chaves ($STATE_DIR)? [s/N] " resp
case "$resp" in
	[sS]*)
		rm -rf "$CONFIG_DIR" "$STATE_DIR"
		echo "Configuração e estado removidos."
		;;
	*)
		echo "Configuração e estado preservados em $CONFIG_DIR e $STATE_DIR."
		;;
esac

echo "==> Desinstalado."
