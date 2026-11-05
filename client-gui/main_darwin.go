//go:build darwin

package main

// govpn client-gui for macOS: a menu-bar (tray) app that mirrors the
// Windows tray client's UX (same icon, same menu: Status/Reconectar
// agora/Desconectar/Abrir configuração/Abrir pasta de logs/Sair) as
// closely as macOS's security model allows — WITHOUT asking for an
// admin password every time it connects.
//
// Why the architecture differs from Windows: on Windows the whole
// process (tray UI + the code that creates the network interface)
// runs elevated via a Scheduled Task — one process does both. On
// macOS that's impossible: a process running as root cannot attach to
// the logged-in user's WindowServer session (no menu bar item), and a
// process attached to that session cannot be root. An earlier version
// of this app solved that by re-elevating the CLI on every connect
// attempt via `osascript ... with administrator privileges` — it
// worked, but meant a password prompt every retry, which is intrusive
// and easy to mistake for malware. This version instead relies on a
// privileged background SERVICE set up ONCE, at install time (the
// .pkg installer already runs as root, so no extra prompt is needed
// beyond that single install step): a LaunchDaemon running
// `govpn-client daemon` as root, forever, from boot — see
// client/main.go's cmdDaemon. This app is just a thin, unprivileged
// viewer for that daemon's state: it polls a status file the daemon
// writes, and sends "reconnect"/"disconnect" requests by touching
// files in a shared directory (both under
// /Library/Application Support/govpn, made group-writable at install
// time so a normal user account — no admin rights needed day to day —
// can read/write them; see pkg-scripts/postinstall).
import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/getlantern/systray"

	"govpn/client/core"
)

//go:embed assets/icon_connected.png
var iconConnected []byte

//go:embed assets/icon_disconnected.png
var iconDisconnected []byte

var (
	mStatus     *systray.MenuItem
	mReconnect  *systray.MenuItem
	mDisconnect *systray.MenuItem
	mOpenConfig *systray.MenuItem
	mOpenLogs   *systray.MenuItem
	mQuit       *systray.MenuItem
)

// sharedDir is where the root daemon (govpn-client daemon, started by
// the LaunchDaemon at boot) and this unprivileged tray app meet: the
// daemon writes config.yml (read here, created if missing) and
// status.json (read-only from here), and this app touches
// reconnect/disconnect request files the daemon polls for. Set up
// group-writable (staff) by the .pkg installer so no admin password
// is needed for any of this day to day — see pkg-scripts/postinstall.
const sharedDir = "/Library/Application Support/govpn"

var (
	configPath     = filepath.Join(sharedDir, "config.yml")
	statusPath     = filepath.Join(sharedDir, "status.json")
	reconnectFile  = filepath.Join(sharedDir, "reconnect")
	disconnectFile = filepath.Join(sharedDir, "disconnect")
)

const pollInterval = 2 * time.Second
const staleAfter = 20 * time.Second // daemon heartbeat should land every 5s; this long without one => treat as not running

func main() {
	// Single-instance guard (Windows uses a named Mutex; flock on our
	// own lock file is the portable equivalent).
	dir, err := core.StateDir()
	if err != nil {
		dir = os.TempDir()
	}
	os.MkdirAll(dir, 0700)
	lf, err := os.OpenFile(filepath.Join(dir, "gui.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err == nil {
		if ferr := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); ferr != nil {
			return // another instance already holds the lock
		}
	}

	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(iconDisconnected)
	systray.SetTitle("")
	systray.SetTooltip("govpn - iniciando...")

	mStatus = systray.AddMenuItem("Iniciando...", "estado atual")
	mStatus.Disable()
	systray.AddSeparator()
	mReconnect = systray.AddMenuItem("Reconectar agora", "Pede pro serviço do govpn tentar conectar de novo")
	mDisconnect = systray.AddMenuItem("Desconectar", "Pede pro serviço do govpn parar o túnel")
	mOpenConfig = systray.AddMenuItem("Abrir configuração", "Edita "+configPath)
	mOpenLogs = systray.AddMenuItem("Abrir pasta de logs", "")
	systray.AddSeparator()
	mQuit = systray.AddMenuItem("Sair", "Fecha o ícone (o túnel continua rodando em segundo plano)")

	ensureConfigExists()

	go pollLoop()
	go menuLoop()
}

func menuLoop() {
	for {
		select {
		case <-mReconnect.ClickedCh:
			touch(reconnectFile)
		case <-mDisconnect.ClickedCh:
			touch(disconnectFile)
		case <-mOpenConfig.ClickedCh:
			ensureConfigExists()
			exec.Command("open", "-a", "TextEdit", configPath).Start()
		case <-mOpenLogs.ClickedCh:
			if dir, err := core.StateDir(); err == nil {
				os.MkdirAll(dir, 0700)
				exec.Command("open", dir).Start()
			}
			exec.Command("open", sharedDir).Start()
		case <-mQuit.ClickedCh:
			// Deliberately does NOT touch disconnectFile: quitting the
			// icon only closes this viewer, same as closing Finder
			// doesn't unmount your disks. The VPN is a system service
			// (like the Linux systemd unit), not tied to this UI's
			// lifetime. Use "Desconectar" first if you actually want
			// the tunnel down.
			systray.Quit()
			return
		}
	}
}

// pollLoop refreshes the tray icon/tooltip/menu from the daemon's
// status.json — pure read-only polling, no privileged action ever
// happens in this process.
func pollLoop() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		sf, err := readStatus()
		switch {
		case err != nil:
			setDisconnected("Serviço do govpn não respondeu ainda")
		case time.Since(sf.UpdatedAt) > staleAfter:
			setDisconnected("Serviço do govpn sem resposta (verifique se está instalado)")
		case sf.Connected:
			systray.SetIcon(iconConnected)
			systray.SetTooltip("govpn - conectado (" + sf.AssignedIP + ")")
			mStatus.SetTitle("Conectado: " + sf.AssignedIP)
		case sf.LastError != "":
			setDisconnected("Erro: " + sf.LastError)
		default:
			setDisconnected("Desconectado")
		}
	}
}

func setDisconnected(msg string) {
	systray.SetIcon(iconDisconnected)
	systray.SetTooltip("govpn - " + msg)
	mStatus.SetTitle(msg)
}

func onExit() {}

type daemonStatus struct {
	Connected  bool      `json:"connected"`
	AssignedIP string    `json:"assigned_ip,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	PID        int       `json:"pid"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func readStatus() (*daemonStatus, error) {
	b, err := os.ReadFile(statusPath)
	if err != nil {
		return nil, err
	}
	var s daemonStatus
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// touch creates the file (if missing) or bumps its mtime — either way
// the daemon's poller (which only cares about ModTime moving forward)
// picks it up within ~2s. No privilege needed: sharedDir is
// group-writable by "staff" (every standard macOS user account).
func touch(path string) {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		os.WriteFile(path, []byte(fmt.Sprintf("%d\n", now.UnixNano())), 0664)
	}
}

// ensureConfigExists writes a starter config.yml (same shape/fields as
// Linux/Windows) if nothing is there yet, so "Abrir configuração"
// always has something to edit, and the daemon has a clear, specific
// error to show instead of silently doing nothing.
func ensureConfigExists() {
	if _, err := os.Stat(configPath); err == nil {
		return
	}
	os.MkdirAll(sharedDir, 0775)
	hn, _ := os.Hostname()
	content := "server: \"http://SEU_SERVIDOR:28443\"\n" +
		"authkey: \"SUA_AUTH_KEY\"\n" +
		"hostname: \"" + hn + "\"\n" +
		"iface: \"utun\"\n" +
		"poll_interval: \"10s\"\n"
	os.WriteFile(configPath, []byte(content), 0664)
}
