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

- ~~Sem NAT traversal avançado~~ — resolvido com um relay simples
  embutido no próprio servidor de coordenação (ver seção "Relay"
  abaixo). Continua sem STUN/hole-punching coordenado — a prioridade é
  sempre o caminho direto, e o relay é o fallback quando ele falha.
  O "beacon" ainda faz a descoberta de endpoint tipo STUN simplificada
  de antes (servidor observa IP:porta de origem da requisição HTTP +
  porta UDP informada pelo cliente) para tentar o caminho direto
  primeiro — ele funciona bem com NAT cone simples, e quando falha
  (NAT simétrico/CGNAT, comum em 4G e alguns provedores residenciais)
  o relay garante que a conexão aconteça de qualquer forma, só que
  passando pelo servidor em vez de P2P direto.
- Sem ACLs / controle de acesso por usuário — qualquer dispositivo que
  tenha uma auth key entra na malha e enxerga todo mundo (full mesh).
- IPs são alocados sequencialmente e nunca reciclados nesse MVP.

## Segurança

Fiz uma auditoria de segurança completa no servidor e no cliente.
Resumo do que foi encontrado e corrigido:

1. **Tudo trafegava em HTTP puro, sem TLS.** Senha de admin, auth keys e
   tokens de dispositivo cruzavam a rede sem criptografia nenhuma.
   **Corrigido:** o servidor agora fala HTTPS por padrão — se você não
   passar `--tls-cert`/`--tls-key` (um certificado de verdade), ele gera
   e reutiliza um certificado autoassinado automaticamente (guardado ao
   lado do banco de dados). Os clientes precisam da flag `--insecure`
   (CLI) ou `insecure_tls: true` (YAML) pra aceitar esse certificado
   autoassinado — isso ainda criptografa a conexão, só não valida a
   identidade do servidor contra uma CA, então prefira um certificado de
   verdade (ex: via reverse proxy com Let's Encrypt) para uso exposto à
   internet de forma mais séria. Pra desligar o TLS totalmente (ex: atrás
   de um proxy que já termina TLS), use `--no-tls`.

   **Importante sobre `insecure_tls` num dispositivo que já tem perfil
   salvo:** o cliente só "liga" essa opção, nunca desliga sozinho — uma
   vez setada (via `--insecure` ou `insecure_tls: true`), ela fica salva
   no `state.json`/`.lnk` e continua valendo nos próximos restarts sem
   precisar repetir a flag. Mas isso significa que, num dispositivo que
   JÁ tinha um perfil registrado ANTES de você habilitar TLS no
   servidor, é preciso editar a config e deixar o serviço reiniciar
   pelo menos uma vez com `insecure_tls: true` presente — só editar e
   nunca reiniciar o serviço não aplica a mudança.

2. **Bypass de autenticação grave no `/api/register`.** Se um
   dispositivo com uma `pub_key` já registrada tentasse se registrar de
   novo, o servidor devolvia o token dele **sem checar a auth key**.
   Como toda `pub_key` é visível pra outros peers via `/api/peers`,
   qualquer membro da malha (ou qualquer um que soubesse/adivinhasse uma
   pubkey) podia roubar o token de outro dispositivo só reenviando um
   registro com essa pubkey. **Corrigido:** uma auth key válida agora é
   exigida em toda chamada a `/api/register`, inclusive quando a pubkey
   já existe (nesse caso o token é rotacionado, mantendo o mesmo IP).

3. **Auth keys e tokens de dispositivo guardados em texto puro no
   SQLite.** Se o arquivo `.db` (ou um backup dele) vazasse, os
   segredos já estavam prontos pra uso direto. **Corrigido:** agora só o
   hash SHA-256 é guardado — a chave/token em si só é mostrado uma vez,
   na hora da criação. Isso muda um pouco o painel admin: a lista de
   auth keys mostra só o prefixo (ex: `govpn-AbCd12…`), e revogar passa a
   ser por ID em vez da chave completa. **Migração automática**: se você
   já tinha um banco criado pela versão antiga, o servidor detecta o
   schema legado no primeiro start com o binário novo e migra sozinho
   (calculando os hashes a partir dos valores antigos, sem perder nenhum
   dispositivo/chave e sem exigir re-registro dos clientes já
   conectados — testei esse fluxo de migração de ponta a ponta com dados
   reais antes de liberar).

4. **Sem proteção contra força bruta no login do painel admin.**
   **Corrigido:** depois de 8 tentativas erradas da mesma origem em 5
   minutos, novas tentativas são bloqueadas (`429`) até a janela passar.

5. **Senha de admin só podia ser passada por flag de linha de comando**
   (`--admin-pass`), visível pra qualquer outro usuário local via
   `ps aux`/histórico do shell/`docker inspect`. **Corrigido:** agora
   também aceita `--admin-pass-file /caminho/para/arquivo`, que não
   aparece em nenhuma lista de processos.

6. **Sem headers de segurança HTTP.** **Corrigido:** toda resposta do
   servidor agora inclui `X-Content-Type-Options`, `X-Frame-Options`,
   `Content-Security-Policy`, `Referrer-Policy` e, quando TLS está
   ativo, `Strict-Transport-Security`. O cookie de sessão do admin
   também ganhou a flag `Secure` quando servindo por HTTPS.

7. **Imagem Docker roda como root dentro do container** (tradeoff
   consciente, não uma correção pendente): a alternativa mais segura
   (`distroless:nonroot`) exige que você mesmo rode um `chown` no
   diretório `./data` do host antes do primeiro `docker compose up`,
   porque bind mounts herdam a permissão do host, não da imagem. Se
   quiser essa configuração mais restrita, troque a tag no `Dockerfile`
   para `gcr.io/distroless/static-debian12:nonroot` e rode
   `sudo chown -R 65532:65532 ./data` antes de subir o container.

Tudo isso foi testado de verdade (não só revisado por código): rodei o
servidor, tentei o ataque de bypass antes e depois da correção,
confirmei por `grep` binário que os segredos não aparecem mais em texto
puro no `.db`, testei o rate limiting fazendo 10 tentativas de login
seguidas, e testei a migração de schema com um banco legado real
(auth key e device token antigos continuam funcionando depois de
migrados).

## Relay

Resolve o caso mais comum de "os dois PCs mostram conectado no painel,
mas o ping entre eles não passa": os dois estão atrás de NAT (redes
residenciais, 4G, CGNAT) e nenhum dos dois tem porta encaminhada no
roteador, então o servidor nunca consegue adivinhar um par IP:porta
onde um realmente alcance o outro via UDP direto.

**Como funciona:** o próprio `govpn-server` aceita uma conexão HTTP
"upgradeada" (`/api/relay`, autenticada com o token do dispositivo) e
mantém ela aberta por tanto tempo quanto o cliente ficar rodando. Cada
pacote que o WireGuard tentaria mandar direto pro peer é **também**
enviado em paralelo por esse canal — o servidor repassa pro peer de
destino (se ele também estiver com o canal de relay aberto), que injeta
o pacote de volta no seu próprio WireGuard como se tivesse chegado
direto. O próprio mecanismo de anti-replay do WireGuard descarta
sozinho a cópia que chegar depois, então não precisa de nenhuma lógica
de "detectar se o direto falhou" — os dois caminhos correm sempre em
paralelo, e o que conseguir chegar primeiro "ganha".

**Tradeoff consciente:** todo pacote trafega duas vezes (direto +
relay) o tempo todo, mesmo quando o caminho direto está funcionando
perfeitamente. Para uma malha pequena (uso pessoal/equipe pequena) isso
é bandwidth desprezível no servidor; não tentei otimizar para parar de
relayar depois que um handshake direto é confirmado (ficaria para uma
v2, se o uso crescer a ponto de a banda do servidor virar problema).

**O que isso NÃO é:** não é um DERP de verdade (o relay do Tailscale) —
não tem múltiplas regiões, não tem fallback entre vários relays, e o
protocolo de enquadramento é uma coisa simples feita pra esse projeto
(não é WebSocket nem nenhum padrão). Funciona, mas é deliberadamente o
mínimo necessário para resolver "NAT simétrico nos dois lados".

Testado de ponta a ponta (não só revisado por código): registrei dois
dispositivos reais contra um servidor de teste, confirmei que uma
mensagem enviada por um chega no outro através do relay (nos dois
sentidos), e confirmei que o `RelayBind` (a peça que liga isso no
wireguard-go) entrega o pacote relayado pro device com o endpoint
correto configurado para aquele peer.

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

Diferente da versão antiga (que tinha uma janela de configuração que
travava em alguns ambientes), essa versão **não tem diálogo nenhum** —
a configuração é só um arquivo YAML, igual ao cliente Linux.

Instalação (no Windows, como Administrador — necessário para criar a
interface de rede, igual o WireGuard oficial):

1. Rode `govpn-setup.exe`.
2. Ele instala em `C:\Program Files\govpn`, cria atalhos no Menu Iniciar
   e na Área de Trabalho, registra um desinstalador em Painel de
   Controle → Programas, cria `C:\ProgramData\govpn\config.yml` (se
   ainda não existir) e, ao final, abre esse arquivo no Bloco de Notas.
3. Edite `C:\ProgramData\govpn\config.yml` com o servidor e a auth key:
   ```yaml
   server: "http://SEU_SERVIDOR:28443"
   authkey: "SUA_AUTH_KEY"
   hostname: ""
   iface: "govpn0"
   poll_interval: "10s"
   ```
4. Pronto — não precisa clicar em nada. O cliente já está rodando em
   segundo plano (instalado como uma Tarefa Agendada que dispara no
   login) e relê esse arquivo sozinho a cada ~15s até conseguir
   conectar. Também sobe automaticamente em todo boot/login dali pra
   frente.

**Por que Tarefa Agendada e não um atalho normal na pasta Startup:**
criar a interface de rede WireGuard exige privilégio de Administrador;
um atalho comum de Startup roda com o token normal (não-elevado) do
usuário e falha com "Acesso negado". A Tarefa Agendada é criada com
`/rl highest` (privilégios mais altos), o que dá o token administrativo
completo sem pedir confirmação do UAC a cada boot — é a mesma técnica
usada por outros softwares que precisam rodar elevados em background.

O ícone na bandeja (perto do relógio — clique na seta "^" se estiver
escondido) muda de cor:

- 🔴 **Vermelho** — desconectado / aguardando configuração válida / erro
- 🟢 **Verde** — conectado, túnel ativo

Passe o mouse sobre o ícone pra ver o IP atribuído, ou clique com o
botão direito para o menu:

- **Status** (linha no topo, só informativa)
- **Reconectar agora** — força uma nova tentativa imediata (sem esperar os ~15s)
- **Abrir configuração** — abre `config.yml` no Bloco de Notas
- **Abrir pasta de logs** — abre `%USERPROFILE%\.govpn` (tem `state.json` e `gui.log`)
- **Sair** — desconecta e fecha o app

A auth key só é usada uma vez, no primeiro registro bem-sucedido —
depois disso o dispositivo fica identificado pelo `state.json` salvo, e
você pode deixar o campo em branco (ou até apagar) no `config.yml`.

Desinstalar: Painel de Controle → Programas → govpn → Desinstalar (ou o
atalho "Desinstalar govpn" no Menu Iniciar). Remove a Tarefa Agendada e
pergunta se quer apagar também a configuração/perfil salvo.

Testado de ponta a ponta num Windows 10 real (VM): instalei via
`govpn-setup.exe`, preenchi o `config.yml` com credenciais reais,
disparei a Tarefa Agendada e confirmei a interface `govpn0` criada de
verdade com IP atribuído pelo servidor, o dispositivo aparecendo
**Online** no painel admin, e o ícone da bandeja mostrando a cor verde
(capturado via sessão RDP). Também confirmei que, sem a elevação via
Tarefa Agendada, a criação da interface falha com "Acesso negado" — por
isso o instalador usa esse mecanismo em vez de um atalho simples.

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

- Porta `28443` publicada no host (diferente da `8443` padrão do binário
  standalone, de propósito — evita conflito comum com outras apps
  open-source que costumam usar 8443/8080/9000/etc)
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

Esse instalador é só para Linux. Windows usa o `govpn-setup.exe`
(instalador com tarefa agendada e config.yml) — veja a seção acima.

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
