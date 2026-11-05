#!/bin/bash
# Diagnóstico do govpn no Mac — cola a saída inteira de volta.
echo "===== 1. LaunchDaemon (serviço root) carregado? ====="
sudo launchctl print system/com.cleberreissosdocs.govpn.client-daemon 2>&1 | head -40

echo
echo "===== 2. LaunchAgent (icone bandeja) carregado? ====="
launchctl print gui/$(id -u)/com.cleberreissosdocs.govpn.client-gui 2>&1 | head -40

echo
echo "===== 3. Pasta compartilhada ====="
ls -la "/Library/Application Support/govpn/" 2>&1

echo
echo "===== 4. config.yml (sem mostrar a auth key inteira) ====="
sed 's/authkey:.*/authkey: "***"/' "/Library/Application Support/govpn/config.yml" 2>&1

echo
echo "===== 5. status.json ====="
cat "/Library/Application Support/govpn/status.json" 2>&1

echo
echo "===== 6. log do serviço (ultimas 60 linhas) ====="
tail -60 "/Library/Application Support/govpn/daemon.log" 2>&1

echo
echo "===== 7. log do icone da bandeja ====="
tail -40 /tmp/govpn-client-gui.log 2>&1

echo
echo "===== 8. interface de rede ====="
ifconfig | grep -A4 utun

echo
echo "===== 9. app instalado? arquitetura certa? ====="
ls -la /Applications/govpn-client-gui.app/Contents/MacOS/ 2>&1
file /Applications/govpn-client-gui.app/Contents/MacOS/govpn-client-gui 2>&1
file /Applications/govpn-client-gui.app/Contents/MacOS/govpn-client 2>&1

echo
echo "===== FIM — cole tudo isso de volta ====="
