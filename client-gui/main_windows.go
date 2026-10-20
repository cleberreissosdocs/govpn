//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"

	"govpn/client/core"
)

//go:embed assets/icon_connected.ico
var iconConnected []byte

//go:embed assets/icon_disconnected.ico
var iconDisconnected []byte

var (
	eng         *core.Engine
	mStatus     *systray.MenuItem
	mReconnect  *systray.MenuItem
	mOpenConfig *systray.MenuItem
	mOpenLogs   *systray.MenuItem
	mQuit       *systray.MenuItem
)

// systemConfigPath is where the installer drops an editable YAML config
// (server/authkey/hostname) — system-wide, so it works no matter which
// Windows user logs in. Same shape/fields as the Linux config.yml.
const systemConfigPath = `C:\ProgramData\govpn\config.yml`

const reconnectRetryInterval = 15 * time.Second

func main() {
	// Single-instance guard: without this, each launch (Startup folder,
	// a stray double-click, autostart racing a manual start) stacks up
	// a brand-new tray icon. If another instance is already running we
	// just exit quietly — the existing tray icon is the only UI anyone
	// needs.
	mutexName, _ := windows.UTF16PtrFromString(`Global\govpn-client-gui-singleton`)
	h, err := windows.CreateMutex(nil, false, mutexName)
	if err == nil && windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		return
	}
	_ = h

	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(iconDisconnected)
	systray.SetTitle("")
	systray.SetTooltip("govpn - iniciando...")

	mStatus = systray.AddMenuItem("Iniciando...", "estado atual")
	mStatus.Disable()
	systray.AddSeparator()
	mReconnect = systray.AddMenuItem("Reconectar agora", "Relê a configuração e tenta conectar imediatamente")
	mOpenConfig = systray.AddMenuItem("Abrir configuração", "Edita "+systemConfigPath)
	mOpenLogs = systray.AddMenuItem("Abrir pasta de logs", "")
	systray.AddSeparator()
	mQuit = systray.AddMenuItem("Sair", "Fechar o govpn")

	eng = core.NewEngine(core.DefaultIfaceName(), 10*time.Second)
	eng.OnStateChange = updateUI
	eng.OnLog = appendLog
	eng.TryLoadSaved() // reuse a previously saved profile if this isn't the first run

	go connectLoop()
	go menuLoop()
}

func menuLoop() {
	for {
		select {
		case <-mReconnect.ClickedCh:
			go attemptConnectOnce()
		case <-mOpenConfig.ClickedCh:
			ensureConfigExists()
			runCmd("notepad.exe", systemConfigPath)
		case <-mOpenLogs.ClickedCh:
			if dir, err := core.StateDir(); err == nil {
				os.MkdirAll(dir, 0700)
				runCmd("explorer.exe", dir)
			}
		case <-mQuit.ClickedCh:
			eng.Disconnect()
			systray.Quit()
			return
		}
	}
}

// connectLoop keeps retrying in the background until it manages to
// connect — there is no manual "configure and click connect" step
// anymore, matching the Linux systemd service's behavior: fill
// config.yml, the client just picks it up on its own (next retry here,
// or next restart).
func connectLoop() {
	for {
		if err := attemptConnectOnce(); err == nil {
			return
		}
		time.Sleep(reconnectRetryInterval)
	}
}

func attemptConnectOnce() error {
	if eng.IsRunning() {
		return nil
	}
	if !eng.HasProfile() {
		if err := loadFromFileConfig(); err != nil {
			setWaitingStatus(err)
			return err
		}
	} else {
		// Device already registered: still re-read config.yml for
		// insecure_tls on every attempt (upgrade-only — never turns it
		// back off) so that enabling it after the fact on an existing
		// install actually takes effect, instead of silently requiring
		// a full re-register like server/authkey/hostname do.
		if cfg, err := core.LoadFileConfig(systemConfigPath); err == nil && cfg != nil {
			eng.UpgradeInsecureTLS(cfg.InsecureTLS)
		}
	}
	if err := eng.Connect(); err != nil {
		setWaitingStatus(err)
		return err
	}
	return nil
}

// loadFromFileConfig registers a brand-new device using whatever is in
// systemConfigPath. Only runs once per install: after a successful
// register, the device's own saved state (core.StateDir()/state.json)
// is used from then on, so editing authkey afterwards has no effect
// (same one-time-use semantics as the Linux client).
func loadFromFileConfig() error {
	cfg, err := core.LoadFileConfig(systemConfigPath)
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("config não encontrado em %s", systemConfigPath)
	}
	if cfg.Server == "" || cfg.AuthKey == "" {
		return fmt.Errorf("preencha server e authkey em %s", systemConfigPath)
	}
	hostname := cfg.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	iface := cfg.Iface
	if iface != "" {
		eng.IfaceName = iface
	}
	if d := core.ParsePollInterval(cfg.PollInterval, 0); d > 0 {
		eng.PollInterval = d
	}
	eng.InsecureTLS = cfg.InsecureTLS
	return eng.RegisterNew(cfg.Server, cfg.AuthKey, hostname)
}

func setWaitingStatus(err error) {
	systray.SetIcon(iconDisconnected)
	msg := "Aguardando configuração"
	if err != nil {
		msg = "Erro: " + err.Error()
	}
	systray.SetTooltip("govpn - " + msg)
	mStatus.SetTitle(msg)
	appendLog(msg)
}

func updateUI(connected bool) {
	if connected {
		systray.SetIcon(iconConnected)
		st := eng.State()
		ip := ""
		if st != nil {
			ip = st.AssignedIP
		}
		systray.SetTooltip("govpn - conectado (" + ip + ")")
		mStatus.SetTitle("Conectado: " + ip)
	} else {
		systray.SetIcon(iconDisconnected)
		systray.SetTooltip("govpn - desconectado")
		mStatus.SetTitle("Desconectado")
		// Connect() dropped (peer sync loop stopped, interface torn
		// down); keep trying to get back online instead of staying
		// dark forever.
		go connectLoop()
	}
}

func onExit() {}

func appendLog(line string) {
	dir, err := core.StateDir()
	if err != nil {
		return
	}
	os.MkdirAll(dir, 0700)
	f, err := os.OpenFile(filepath.Join(dir, "gui.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
}

// ensureConfigExists writes a starter config.yml (with explanatory
// comments) if the installer hasn't already — makes "Abrir
// configuração" work even if something deleted the file after install.
func ensureConfigExists() {
	if _, err := os.Stat(systemConfigPath); err == nil {
		return
	}
	os.MkdirAll(filepath.Dir(systemConfigPath), 0700)
	hn, _ := os.Hostname()
	content := "server: \"http://SEU_SERVIDOR:28443\"\n" +
		"authkey: \"SUA_AUTH_KEY\"\n" +
		"hostname: \"" + hn + "\"\n" +
		"iface: \"govpn0\"\n" +
		"poll_interval: \"10s\"\n"
	os.WriteFile(systemConfigPath, []byte(content), 0600)
}

func runCmd(name string, args ...string) {
	exec.Command(name, args...).Start()
}
