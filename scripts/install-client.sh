#!/bin/bash
# install-client.sh — instala o govpn-client no Linux como serviço do
# sistema (systemd), com um arquivo de configuração YAML editável e
# início automático no boot.
#
# Uso:
#   sudo ./install-client.sh [caminho/para/govpn-client]
#
# Se nenhum caminho for passado, procura um binário "govpn-client" na
# mesma pasta deste script.

set -euo pipefail

INSTALL_DIR="/opt/govpn"
CONFIG_DIR="/etc/govpn"
STATE_DIR="/var/lib/govpn"
BIN_NAME="govpn-client"
SERVICE_NAME="govpn-client"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ "$(id -u)" -ne 0 ]; then
	echo "Precisa rodar como root (sudo ./install-client.sh)." >&2
	exit 1
fi

SRC_BIN="${1:-$SCRIPT_DIR/$BIN_NAME}"
if [ ! -f "$SRC_BIN" ]; then
	echo "Binário não encontrado: $SRC_BIN" >&2
	echo "Passe o caminho explicitamente: sudo ./install-client.sh /caminho/para/govpn-client" >&2
	exit 1
fi

echo "==> Instalando binário em $INSTALL_DIR/$BIN_NAME"
mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" "$STATE_DIR"
cp "$SRC_BIN" "$INSTALL_DIR/$BIN_NAME"
chmod 755 "$INSTALL_DIR/$BIN_NAME"

CONFIG_FILE="$CONFIG_DIR/config.yml"
if [ -f "$CONFIG_FILE" ]; then
	echo "==> Config existente mantida: $CONFIG_FILE (não sobrescrevi)"
else
	echo "==> Criando config de exemplo em $CONFIG_FILE"
	DEFAULT_HOSTNAME="$(hostname)"
	cat > "$CONFIG_FILE" <<EOF
# Configuração do govpn-client.
# Preencha server e authkey (gerados no painel admin do govpn-server,
# em /admin) e depois rode:
#   sudo systemctl restart govpn-client
#
# Depois do primeiro registro bem-sucedido, o dispositivo fica
# identificado pela chave salva em $STATE_DIR/state.json — você pode
# deixar authkey em branco nas próximas vezes (ela só é usada uma vez,
# no primeiro registro).

server: "http://SEU_SERVIDOR:8443"
authkey: "SUA_AUTH_KEY"
hostname: "$DEFAULT_HOSTNAME"
iface: "govpn0"
poll_interval: "10s"
EOF
	chmod 600 "$CONFIG_FILE"
fi

echo "==> Criando unit systemd /etc/systemd/system/${SERVICE_NAME}.service"
cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=govpn client (mesh VPN)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/$BIN_NAME up --config $CONFIG_FILE
Environment=GOVPN_STATE_DIR=$STATE_DIR
Restart=on-failure
RestartSec=5
# o cliente precisa criar uma interface de rede (TUN) e configurá-la
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "${SERVICE_NAME}.service"

cat <<EOF

==> Instalado com sucesso.

Próximos passos:
  1. Edite $CONFIG_FILE com o endereço do seu servidor e a auth key.
  2. Inicie o serviço:   sudo systemctl start ${SERVICE_NAME}
  3. Acompanhe os logs:  sudo journalctl -u ${SERVICE_NAME} -f
  4. Veja o status:      sudo systemctl status ${SERVICE_NAME}

O serviço já está habilitado (systemctl enable) e vai subir sozinho no
próximo boot. Pra desinstalar, use o uninstall-client.sh ao lado deste
script.
EOF
