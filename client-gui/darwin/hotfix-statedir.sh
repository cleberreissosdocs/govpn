#!/bin/bash
# hotfix-govpn-statedir.sh — corrige uma instalação já feita do
# govpn-client-gui ANTES de 2026-11-05 (o LaunchDaemon não tinha
# GOVPN_STATE_DIR definido, então o serviço nunca conseguia salvar
# state.json rodando como root — cada tentativa "queimava" a auth key
# de novo, até dar "auth key already used").
#
# Isso só corrige o ambiente do serviço; depois de rodar, ainda é
# necessário colocar uma auth key NOVA no config.yml (a antiga já foi
# usada e não serve mais) — veja a mensagem no final.
#
# Uso: sudo bash hotfix-govpn-statedir.sh

set -euo pipefail

DAEMON_PLIST="/Library/LaunchDaemons/com.cleberreissosdocs.govpn.client-daemon.plist"
SHARED_DIR="/Library/Application Support/govpn"

if [ "$(id -u)" -ne 0 ]; then
	echo "Rode com sudo: sudo bash hotfix-govpn-statedir.sh" >&2
	exit 1
fi

if [ ! -f "$DAEMON_PLIST" ]; then
	echo "Não encontrei $DAEMON_PLIST — o serviço não parece instalado." >&2
	exit 1
fi

if grep -q GOVPN_STATE_DIR "$DAEMON_PLIST"; then
	echo "Já está com GOVPN_STATE_DIR definido — nada a corrigir aqui."
else
	echo "==> Adicionando GOVPN_STATE_DIR=$SHARED_DIR ao serviço"
	/usr/libexec/PlistBuddy -c "Add :EnvironmentVariables:GOVPN_STATE_DIR string '$SHARED_DIR'" "$DAEMON_PLIST"
fi

echo "==> Apagando qualquer state.json órfão de tentativas anteriores (sem HOME, nunca salvou mesmo)"
rm -f "/var/root/.govpn/state.json" 2>/dev/null || true

echo "==> Reiniciando o serviço"
launchctl bootout system "$DAEMON_PLIST" 2>/dev/null || true
launchctl bootstrap system "$DAEMON_PLIST" 2>/dev/null || launchctl load "$DAEMON_PLIST"

echo
echo "Pronto. AGORA falta uma auth key NOVA (a antiga foi consumida):"
echo "1. No navegador: https://govpn.sosdocs.com.br/admin -> gerar nova auth key"
echo "2. Edite $SHARED_DIR/config.yml e cole a chave nova no campo authkey"
echo "3. Rode: touch \"$SHARED_DIR/reconnect\""
echo "   (ou espere ~15s, o serviço tenta sozinho)"
