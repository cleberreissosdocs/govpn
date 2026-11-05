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

## Instalação

Tudo que você precisa pra subir o servidor e conectar máquinas na malha,
sem precisar compilar nada (os binários/instaladores já estão publicados
em [Releases](https://github.com/cleberreissosdocs/govpn/releases/latest)).
Quem quiser compilar do zero encontra os comandos na seção **Build a
partir do código-fonte**, mais abaixo.

### 1. Servidor (Ubuntu, uma vez só)

Mais simples: Docker.

```bash
git clone https://github.com/cleberreissosdocs/govpn.git && cd govpn
# edite docker-compose.yml e troque --admin-pass pela sua senha
docker compose up -d --build
```

Sobe em `http://SEU_SERVIDOR:28443`, com o banco persistido em
`./data/govpn.db` (sobrevive a recriação do container) e reinício
automático se o processo cair.

Gere uma auth key pra cada dispositivo novo (é ela que o cliente usa pra
se registrar na malha):

```bash
docker compose exec govpn-server /usr/local/bin/govpn-server genkey --db /data/govpn.db --note "notebook joão"
```

Acesse o painel em `http://SEU_SERVIDOR:28443/admin` (veja a seção
**Painel de administração** abaixo). Sem Docker? Veja **Servidor sem
Docker (systemd)** mais abaixo.

### 2. Cliente Windows

Baixe e rode como Administrador:
[**govpn-setup.exe**](https://github.com/cleberreissosdocs/govpn/releases/latest/download/govpn-setup.exe)

O instalador abre `C:\ProgramData\govpn\config.yml` no Bloco de Notas no
final — preencha e salve:

```yaml
server: "http://SEU_SERVIDOR:28443"
authkey: "SUA_AUTH_KEY"
```

Pronto — não precisa clicar em mais nada. O cliente já fica rodando em
segundo plano (sobe sozinho a cada login) e relê esse arquivo a cada
~15s até conectar. Detalhes do ícone de bandeja e desinstalação na
seção **Cliente gráfico para Windows** mais abaixo.

### 3. Cliente Linux

Mais simples: pacote `.deb` (Debian/Ubuntu), direto do GitHub:

```bash
curl -fsSL -o govpn-client.deb \
  https://github.com/cleberreissosdocs/govpn/releases/latest/download/govpn-client_1.0.0_amd64.deb
sudo dpkg -i govpn-client.deb
sudo nano /etc/govpn/config.yml   # preencha server + authkey
sudo systemctl start govpn-client
```

Formato do `config.yml`:

```yaml
server: "http://SEU_SERVIDOR:28443"
authkey: "SUA_AUTH_KEY"
hostname: "nome-do-dispositivo"
iface: "govpn0"
poll_interval: "10s"
```

Conferir se está rodando:

```bash
systemctl status govpn-client        # ativo/inativo
sudo journalctl -u govpn-client -f   # logs em tempo real
ip link show govpn0                  # interface criada = conectado
```

Desinstalar mantendo a config: `sudo apt remove govpn-client`.
Removendo tudo (config + chaves): `sudo apt purge govpn-client`.

Não é Debian/Ubuntu, ou prefere sem pacote? Veja **Instalação Linux via
script** mais abaixo (mesmo resultado — serviço systemd — a partir de um
binário avulso).

### 4. Cliente macOS

App de bandeja igual ao do Windows (ícone verde/vermelho, menu com
Status/Reconectar/Abrir configuração/Abrir pasta de logs/Sair),
compilado num runner macOS de verdade pelo GitHub Actions (não dá pra
cross-compilar isso do Linux, a lib do ícone de bandeja usa Cocoa):
[**govpn-client-gui-macos.zip**](https://github.com/cleberreissosdocs/govpn/releases/latest/download/govpn-client-gui-macos.zip)

Instalação:

```bash
unzip govpn-client-gui-macos.zip
./install-client-gui-macos.sh
```

Diferença importante em relação ao Windows: no Windows o app inteiro
roda como Administrador (a Tarefa Agendada sobe assim), então criar a
interface de rede é transparente. No macOS isso não é possível — um
processo root não consegue mostrar ícone na barra de menu. Por isso
este app roda sem privilégio nenhum e, só na hora de conectar, pede
sua senha de administrador pelo diálogo padrão do macOS (é a mesma
senha da sua conta). Se você cancelar esse diálogo, o app não fica
tentando de novo sozinho a cada 15s (evita ficar martelando o pedido de
senha) — use "Reconectar agora" no menu quando quiser tentar de novo.

Preferir linha de comando em vez do app gráfico? Veja **Build a partir
do código-fonte** mais abaixo pra compilar o `govpn-client` CLI puro.

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

## Build a partir do código-fonte

Só é necessário se você quer compilar você mesmo (dev, arquitetura sem
binário publicado, ou customização). Pra instalar normalmente, use a
seção **Instalação** lá em cima.

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

Cliente gráfico macOS (bandeja do sistema): **não cross-compila daqui**
— usa cgo + Cocoa (`getlantern/systray`), que exige o toolchain real do
Xcode. Só builda numa máquina macOS de verdade:

```bash
# rodar isso NUM MAC, não aqui:
./client-gui/darwin/build-app.sh   # gera dist/govpn-client-gui-macos.zip
```

O workflow `.github/workflows/macos-client-gui.yml` faz exatamente
isso automaticamente num runner macOS do GitHub Actions a cada release
publicado (ou sob demanda, via "Run workflow").

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

## Cliente gráfico para macOS (barra de menu)

Mesma ideia e o mesmo menu do cliente Windows (`client-gui/main_darwin.go`),
adaptados às diferenças reais de segurança do macOS — leia a limitação
na seção **Instalação** acima antes de instalar.

**Por que o código é estruturalmente diferente do Windows por baixo:**
no Windows o processo inteiro roda elevado (a Tarefa Agendada sobe
assim), então o mesmo processo mostra o ícone E cria a interface de
rede. No macOS um processo root não consegue se conectar à sessão
gráfica do usuário (não é uma permissão, é uma separação dura do
sistema) — então aqui são efetivamente dois processos: o app de
bandeja (sem privilégio nenhum, é ele que aparece na barra de menu) e,
só quando precisa conectar, uma cópia do `govpn-client` (CLI) iniciada
como root via o diálogo de senha padrão do macOS (`osascript ... with
administrator privileges`). Os dois não compartilham memória, então
conversam por dois arquivos pequenos em `~/.govpn/`: um status em JSON
que o processo elevado escreve e a bandeja só lê (`gui_status.json`), e
um pidfile para a bandeja conseguir pedir pra ele parar
(`gui_child.pid`).

Instalação (gera o `.app` num runner macOS do GitHub Actions — veja
**Instalação** acima):

1. Baixe e extraia `govpn-client-gui-macos.zip`.
2. Rode `./install-client-gui-macos.sh` (copia o app pra
   `/Applications`, registra um LaunchAgent em
   `~/Library/LaunchAgents` que sobe o app — sem privilégio — a cada
   login).
3. Clique com o botão direito no ícone da bandeja → **Abrir
   configuração**, preencha:
   ```yaml
   server: "http://SEU_SERVIDOR:28443"
   authkey: "SUA_AUTH_KEY"
   ```
   salve e feche o TextEdit.
4. O app detecta o arquivo salvo e tenta conectar sozinho em
   instantes — nesse momento o macOS mostra o diálogo pedindo sua senha
   de administrador (é o passo que cria a interface `utun`). Depois de
   digitar, a interface sobe e o ícone fica verde.

O ícone (perto do relógio) muda de cor exatamente como no Windows:

- 🔴 **Vermelho** — desconectado / aguardando configuração válida / erro
- 🟢 **Verde** — conectado, túnel ativo

Menu (clique no ícone):

- **Status** (linha no topo, só informativa)
- **Reconectar agora** — força uma nova tentativa (pode pedir a senha de novo)
- **Desconectar** — para o túnel sem fechar o ícone da bandeja (pede senha)
- **Abrir configuração** — abre `~/Library/Application Support/govpn/config.yml` no TextEdit
- **Abrir pasta de logs** — abre `~/.govpn` no Finder (tem `state.json`, `gui_status.json`, `gui_child.log`)
- **Sair** — tenta parar o túnel (pede senha se estiver rodando) e fecha o ícone

A auth key só é usada no primeiro registro bem-sucedido — depois disso
o dispositivo fica identificado pelo `state.json` salvo em `~/.govpn`
(o app cuida de apontar o processo elevado pra esse mesmo diretório via
`GOVPN_STATE_DIR`, já que o `$HOME` de um processo root não é o seu).

Diferença de comportamento em relação ao Windows (consciente, não bug):
se você cancelar o diálogo de senha, o app NÃO fica repetindo o pedido
a cada 15 segundos — ficaria indistinguível de malware tentando
escalar privilégio. Em vez disso ele só tenta de novo quando você
clica em "Reconectar agora" ou edita o `config.yml` de novo.

Desinstalar: rode `uninstall-client-gui-macos.sh` (vem no mesmo zip) —
para o túnel, remove o LaunchAgent e o app, e pergunta se quer apagar
config/estado também.

> Limitação honesta: não tenho um Mac real aqui pra testar — o código
> foi revisado cuidadosamente e a API da lib de bandeja (`getlantern/
> systray`) conferida contra a documentação, mas o binário real só é
> gerado pelo runner macOS do GitHub Actions, nunca rodado por mim.
> Teste antes de confiar cegamente, e me avise se achar algo quebrado
> (ícone não aparece, diálogo de senha não vem, LaunchAgent não sobe
> no login, etc.).

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

## Servidor sem Docker (systemd)

Alternativa pra quem não quer Docker — mesmo resultado, direto com o
binário:

```bash
sudo mkdir -p /opt/govpn
sudo cp bin/govpn-server /opt/govpn/
```

Crie `/etc/systemd/system/govpn-server.service`:

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

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now govpn-server
sudo ufw allow 8443/tcp     # libere a porta da API
```

Gerar auth key:

```bash
/opt/govpn/govpn-server genkey --db /opt/govpn/govpn.db --note "notebook joão"
```

### Produção: TLS

Por padrão o `serve` sobe HTTP puro. Para expor pela internet, coloque
atrás de um reverse proxy com TLS (nginx/caddy) ou rode com um proxy
`stunnel`, e aponte os clientes para `--server https://seu-dominio:443`.

## Instalação Linux via script (sem pacote .deb)

Alternativa ao `.deb` da seção **Instalação** — útil se sua distro não é
Debian/Ubuntu, ou se prefere um binário avulso em vez de pacote. Mesmo
resultado final (serviço systemd), a partir de `scripts/install-client.sh`:

```bash
sudo ./scripts/install-client.sh                      # usa ./govpn-client ao lado do script
sudo ./scripts/install-client.sh /caminho/pro/binario  # ou aponta o binário explicitamente

# edite /etc/govpn/config.yml com o servidor e a auth key, depois:
sudo systemctl start govpn-client
```

Pra desinstalar: `sudo ./scripts/uninstall-client.sh` (pergunta se quer
apagar config/estado também, ou só remove o serviço e o binário).

Quer empacotar você mesmo como `.deb` (em vez de baixar o pronto dos
Releases)? Use `packaging/deb/build-deb.sh [versão] [arquitetura]` —
gera `bin/govpn-client_<versão>_<arch>.deb` a partir do skeleton em
`packaging/deb/`.
