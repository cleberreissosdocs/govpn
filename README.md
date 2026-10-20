# govpn

Mesh VPN estilo Tailscale, construído sobre WireGuard: um servidor de
coordenação central (roda no Ubuntu) + agentes de cliente (Linux e Windows)
que se auto-registram, recebem um IP da malha e mantêm uma configuração
WireGuard full-mesh sincronizada automaticamente.

Testado ponta a ponta localmente (dois clientes WireGuard reais trocando
handshake e tráfego ICMP por túnel). Faltam testar: cliente Windows em
máquina real (aqui só foi cross-compilado, não executado) e NAT real entre
redes distintas pela internet.

## Limitações do MVP (leia antes de usar em produção)

- Sem NAT traversal avançado (sem DERP/relay como o Tailscale real tem).
  O "beacon" faz uma descoberta de endpoint tipo STUN simplificada (o
  servidor observa o IP:porta de origem da requisição HTTP e assume que a
  porta UDP do WireGuard é a mesma informada pelo cliente). Funciona bem
  quando os peers têm IP público ou estão atrás de NAT cone simples com a
  porta liberada/mapeada. NAT simétrico ou CGNAT provavelmente não vai
  conseguir conectar direto — precisaria de um relay (fica para v2).
- Sem ACLs / controle de acesso por usuário — qualquer dispositivo que
  tenha uma auth key entra na malha e enxerga todo mundo (full mesh).
- Tráfego HTTP do servidor de coordenação por padrão não é TLS (veja seção
  "Produção" abaixo).
- IPs são alocados sequencialmente e nunca reciclados nesse MVP.

## Arquitetura

- `govpn-server`: roda no Ubuntu Server. API REST + SQLite. Gera "auth
  keys" (tipo convite), aloca IPs da faixa 100.64.0.0/10 (mesma faixa do
  Tailscale, não colide com redes domésticas/corporativas comuns), guarda
  chave pública + IP + endpoint observado de cada dispositivo.
- `govpn-client`: roda em cada máquina (Linux ou Windows, precisa de
  root/Administrator pois cria uma interface de rede). Gera seu par de
  chaves WireGuard localmente, registra no servidor, cria a interface
  `govpn0` via wireguard-go (implementação userspace — não depende do
  módulo de kernel WireGuard nem do `wg-quick` estarem instalados) e fica
  em loop sincronizando a lista de peers a cada N segundos.

## Build

Requer Go >= 1.23 (o `go.mod` já aponta a toolchain certa, o `go build`
baixa sozinho se necessário).

```
cd govpn
go build -o bin/govpn-server ./server/
go build -o bin/govpn-client ./client/                      # CLI Linux
GOOS=windows GOARCH=amd64 go build -o bin/govpn-client.exe ./client/   # CLI Windows
GOOS=darwin GOARCH=amd64 go build -o bin/govpn-client-darwin-amd64 ./client/  # CLI macOS Intel
GOOS=darwin GOARCH=arm64 go build -o bin/govpn-client-darwin-arm64 ./client/  # CLI macOS Apple Silicon

# cliente gráfico Windows (bandeja do sistema) + instalador:
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-H=windowsgui" -o bin/govpn-client-gui.exe ./client-gui/
cd client-gui && makensis govpn-installer.nsi   # gera bin/govpn-setup.exe (requer `apt install nsis`)
```

O binário CLI Windows precisa do `wintun.dll` (da WireGuard Foundation,
https://www.wintun.net/) na mesma pasta do `.exe`, ou em
`C:\Windows\System32`. É o mesmo driver que o WireGuard oficial para
Windows usa — não precisa instalar o WireGuard, só esse DLL. O instalador
gráfico (`govpn-setup.exe`) já inclui o `wintun.dll` automaticamente, não
precisa baixar nada à parte.

## Cliente gráfico para Windows (bandeja do sistema)

Em vez do `govpn-client.exe` de linha de comando, Windows tem um cliente
gráfico completo: `govpn-client-gui.exe`, empacotado no instalador
`govpn-setup.exe` (gerado em `bin/` pelo passo do NSIS acima).

Instalação (no Windows, como Administrador — necessário para criar a
interface de rede, igual o WireGuard oficial):

1. Rode `govpn-setup.exe`.
2. Ele instala em `C:\Program Files\govpn`, cria atalho no Menu Iniciar e
   na Área de Trabalho, registra um desinstalador em
   Painel de Controle → Programas, e ao final oferece abrir o app.
3. Na primeira execução, se não houver perfil salvo, abre automaticamente
   a janela "Perfil" pedindo Servidor + Auth Key + Nome do dispositivo.

Uma vez rodando, o app fica como um ícone na bandeja do sistema (perto do
relógio) — azul quando conectado, cinza quando desconectado. Clicar nele
abre um menu com:

- **Status** (linha no topo, só informativa: mostra o IP atribuído quando conectado)
- **Conectar** / **Desconectar**
- **Perfil...** — reabre a janela de configuração a qualquer momento, para
  trocar de servidor/conta (gera um novo par de chaves e registra de novo)
- **Iniciar com o Windows** — checkbox que liga/desliga o início
  automático no login (grava em `HKCU\...\Run`, sem precisar de admin
  para essa parte)
- **Abrir pasta de configuração** — abre o Explorer em
  `%USERPROFILE%\.govpn`, onde ficam o perfil (`state.json`) e o log
  (`gui.log`)
- **Sair** — desconecta e fecha o app

Desinstalar: Painel de Controle → Programas → govpn → Desinstalar (ou o
atalho "Desinstalar govpn" no Menu Iniciar). Pergunta se quer apagar
também o perfil salvo.

Atualização: já testei isso num Windows 10 real (não só cross-compile) — o
cliente de linha de comando (`govpn-client.exe`) cria a interface
WireGuard de verdade, registra no servidor e troca tráfego real com
outros peers (testado com ping cruzando o túnel, 0% de perda). O
cliente gráfico de bandeja (`govpn-client-gui.exe` / `govpn-setup.exe`)
ainda tem um bug não resolvido nos cliques do menu de bandeja quando
testado via sessão RDP — se for usar o Windows, prefira o
`govpn-client.exe` por linha de comando por enquanto.

## Painel de administração

O servidor expõe um painel web em `/admin` (ex: `http://192.168.1.10:8443/admin`):

- Lista de dispositivos conectados (hostname, IP, endpoint observado,
  última atividade com indicador online/offline, chave pública) com botão
  para remover um dispositivo da malha
- Geração e revogação de auth keys direto pela interface

Login: usuário/senha fixos passados como flags na inicialização:

```
./govpn-server serve --db ./govpn.db --addr :8443 --admin-user admin --admin-pass "sua-senha-forte"
```

Se você não passar `--admin-pass`, o servidor gera uma senha aleatória e
imprime nos logs na primeira inicialização (procure por "admin panel
password auto-generated" no log/journal). A sessão de login dura 12h e
fica em memória — reiniciar o servidor derruba todas as sessões ativas
(só precisa logar de novo, nada é perdido).

Por enquanto não tem TLS embutido no painel — exponha `/admin` só na rede
interna/VPN, ou coloque atrás de um reverse proxy com HTTPS se for expor
pela internet (veja a seção de TLS mais abaixo).

## Backup e restauração

Em `/admin/backup` (link "Backup" no topo do painel):

- Configure um diretório no servidor onde os backups serão salvos (ex:
  `/home/lab/govpn/backups`) e, opcionalmente, habilite backup automático
  com um intervalo em minutos.
- **Retenção**: defina quantos backups manter (ex: 10) — a cada novo
  backup, os mais antigos além desse número são apagados automaticamente.
  Deixe em 0 para manter todos para sempre (sem limpeza automática).
- "Fazer backup agora" cria um snapshot consistente do banco inteiro
  (`govpn-backup-AAAAMMDD-HHMMSS.db`) usando `VACUUM INTO` do SQLite —
  seguro de rodar com o servidor no ar, sem downtime.
- A lista de backups existentes no diretório aparece na mesma página,
  com botões "Restaurar" e "Apagar" por arquivo.
- Restaurar fecha a conexão com o banco atual, substitui o arquivo
  pelo backup escolhido e reabre — tudo isso sem reiniciar o processo.
  Qualquer dispositivo/chave criado depois daquele backup é perdido (é
  uma restauração completa, não um merge). Sessões de login no painel
  não são afetadas (ficam em memória, fora do banco).
- O diretório de backup é só uma pasta no filesystem do servidor — faça
  você mesmo a cópia dela para outro lugar (outro disco, outro host) se
  quiser proteção contra perda do servidor inteiro; o recurso aqui só
  cobre "voltar para um estado anterior", não offsite storage.

## Deploy no servidor (Ubuntu Server)

### Opção A: Docker (mais simples)

```
git clone <este repositório> govpn && cd govpn
# edite docker-compose.yml e troque --admin-pass pela sua senha
docker compose up -d --build
```

Isso builda a imagem (multi-stage: compila o Go, resultado final é uma
imagem `distroless` mínima, só o binário + libs do sistema, sem shell/
apt/etc — poucos MB e superfície de ataque pequena) e sobe o container
com:

- Porta `8443` publicada no host
- Banco de dados persistido em `./data/govpn.db` no host (sobrevive a
  `docker compose down` / recriação do container)
- Reinício automático (`restart: unless-stopped`)

Gerar uma auth key com o servidor rodando em container:

```
docker compose exec govpn-server /usr/local/bin/govpn-server genkey --db /data/govpn.db --note "notebook joão"
```

Ver logs:

```
docker compose logs -f
```

Se você quiser configurar backup automático pelo painel (`/admin/backup`),
aponte o diretório de backup para algo DENTRO de `/data` (ex:
`/data/backups`) — assim ele também fica no volume persistido no host,
e não se perde se o container for recriado.

### Opção B: binário direto (systemd)

```
sudo mkdir -p /opt/govpn
sudo cp bin/govpn-server /opt/govpn/
sudo /opt/govpn/govpn-server serve --db /opt/govpn/govpn.db --addr :8443
```

Para rodar como serviço, crie `/etc/systemd/system/govpn-server.service`:

```
[Unit]
Description=govpn coordination server
After=network.target

[Service]
ExecStart=/opt/govpn/govpn-server serve --db /opt/govpn/govpn.db --addr :8443 --admin-user admin --admin-pass CHANGE_ME
Restart=on-failure
User=root

[Install]
WantedBy=multi-user.target
```

```
sudo systemctl daemon-reload
sudo systemctl enable --now govpn-server
sudo ufw allow 8443/tcp     # libere a porta da API
```

Gere uma auth key para cada novo dispositivo (ou uma reutilizável para um
grupo de máquinas confiáveis):

```
/opt/govpn/govpn-server genkey --db /opt/govpn/govpn.db --note "notebook joão"
```

### Produção: TLS

Por padrão o `serve` sobe HTTP puro. Para expor pela internet, coloque
atrás de um reverse proxy com TLS (nginx/caddy) ou rode com um proxy
`stunnel`, e aponte os clientes para `--server https://seu-dominio:443`.

## Uso no cliente

Linux (como root, para poder criar a interface):

```
sudo ./govpn-client up --server http://IP_DO_SERVIDOR:8443 --authkey SUA_AUTH_KEY --hostname meu-notebook
```

Windows (PowerShell como Administrador):

```
.\govpn-client.exe up --server http://IP_DO_SERVIDOR:8443 --authkey SUA_AUTH_KEY --hostname meu-pc
```

macOS (como root — use o binário certo pra sua arquitetura, Intel ou
Apple Silicon; testado em Monterey 12.7.6 via cross-compile, veja a
ressalva logo abaixo):

```
sudo ./govpn-client-darwin-arm64 up --server http://IP_DO_SERVIDOR:8443 --authkey SUA_AUTH_KEY --hostname meu-mac
# Intel: use govpn-client-darwin-amd64 no lugar
```

No macOS a interface criada não se chama `govpn0` — o kernel só aceita
nomes com prefixo `utun` e escolhe o número sozinho (ex: `utun5`). O
cliente já lida com isso automaticamente (não precisa passar `--iface`).
Se o Gatekeeper reclamar que o binário "é de um desenvolvedor não
identificado" (comum em arquivo baixado pela internet, não deve
acontecer se você copiou via `scp`/USB/git), rode
`xattr -d com.apple.quarantine ./govpn-client-darwin-arm64` antes.

Limitação honesta: não tenho uma máquina com macOS real aqui pra testar
— o `govpn-client.exe`/binário Linux eu validei rodando de verdade (veja
o histórico do projeto), mas o binário macOS só foi cross-compilado e
revisado por código, usando exatamente a mesma biblioteca (wireguard-go)
que a implementação oficial usa para `utun` no Darwin. Teste num Mac de
verdade antes de confiar cegamente, e me avise se achar algo quebrado.

Na primeira execução ele registra o dispositivo e salva o estado em
`~/.govpn/state.json` (Linux) ou `%USERPROFILE%\.govpn\state.json`
(Windows). Nas próximas execuções não precisa mais passar `--authkey`:

```
sudo ./govpn-client up
```

Ver status local:

```
./govpn-client status
```

Depois que dois ou mais clientes estiverem rodando, cada um enxerga os
IPs dos outros na faixa 100.64.x.x e consegue `ping` diretamente.

## Instalação do cliente Linux como serviço do sistema

Pra não precisar deixar um terminal aberto rodando `govpn-client up`,
tem um instalador em `scripts/install-client.sh` que:

- Copia o binário pra `/opt/govpn/govpn-client`
- Cria um arquivo de configuração editável em `/etc/govpn/config.yml`
  (permissão `600`, só root lê — protege a auth key)
- Cria e habilita um serviço systemd (`govpn-client.service`) que sobe
  sozinho no boot e reinicia automaticamente se cair

```
sudo ./scripts/install-client.sh                      # usa ./govpn-client ao lado do script
sudo ./scripts/install-client.sh /caminho/pro/binario  # ou aponta o binário explicitamente

# edite /etc/govpn/config.yml com o servidor e a auth key, depois:
sudo systemctl start govpn-client
sudo journalctl -u govpn-client -f    # acompanhar os logs
```

Formato do `config.yml`:

```yaml
server: "http://SEU_SERVIDOR:8443"
authkey: "SUA_AUTH_KEY"
hostname: "nome-do-dispositivo"
iface: "govpn0"
poll_interval: "10s"
```

A auth key só é usada na primeira vez (registro inicial) — depois disso
o dispositivo fica identificado pela chave salva em
`/var/lib/govpn/state.json`, então pode deixar o campo em branco depois
do primeiro `systemctl start` funcionar, se quiser.

Testado de ponta a ponta: rodei o instalador de verdade, configurei o
`config.yml` com um servidor real, subi o serviço via `systemctl start`
e confirmei a interface `govpn0` criada, IP atribuído pelo servidor e
`systemctl is-enabled` mostrando `enabled` (sobe sozinho no boot).

Pra desinstalar: `sudo ./scripts/uninstall-client.sh` (pergunta se quer
apagar config/estado também, ou só remove o serviço e o binário).

Esse instalador é só para Linux. Windows usa o `govpn-client-gui.exe`
(bandeja do sistema, com "Iniciar com o Windows" no menu) ou o instalador
`govpn-setup.exe` — veja a seção acima.

## Pacote .deb (Debian/Ubuntu)

Se preferir um pacote de verdade em vez do script (facilita gerenciar
múltiplas máquinas, atualizar com `apt`/`dpkg`, etc), tem um empacotador
em `packaging/deb/`. Ele faz exatamente a mesma coisa que o
`install-client.sh` (binário em `/usr/bin`, config em `/etc/govpn/`,
serviço systemd em `/lib/systemd/system`), só que embalado como `.deb`
de verdade, com `postinst`/`prerm`/`postrm` cuidando de habilitar o
serviço, proteger a config (`conffiles`, então um `apt upgrade` não
sobrescreve sua auth key) e limpar tudo no `purge`.

```
./packaging/deb/build-deb.sh                # versão 1.0.0, amd64 (builda o binário se precisar)
./packaging/deb/build-deb.sh 1.2.0 arm64     # versão e arquitetura customizadas

sudo dpkg -i bin/govpn-client_1.0.0_amd64.deb
sudo nano /etc/govpn/config.yml              # preenche server + authkey
sudo systemctl start govpn-client
```

Desinstalar mantendo a config: `sudo apt remove govpn-client` (ou
`dpkg -r`). Desinstalar removendo tudo (config + chaves):
`sudo apt purge govpn-client` (ou `dpkg -P`).

Testado de ponta a ponta: buildei o `.deb`, instalei com `dpkg -i` numa
máquina real, confirmei no `dpkg -l` (status `ii`), serviço systemd
habilitado automaticamente, `/etc/govpn/config.yml` criado com
permissão 600, e o `dpkg -P` (purge) removendo tudo direitinho
(config, estado, serviço) sem deixar resíduo.
