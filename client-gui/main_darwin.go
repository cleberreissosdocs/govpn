//go:build darwin

package main

// govpn client-gui for macOS: a menu-bar (tray) app that mirrors the
// Windows tray client's UX (red/green icon, Status/Reconectar
// agora/Abrir configuração/Abrir pasta de logs/Sair menu, autostart,
// config.yml picked up automatically) as closely as macOS's security
// model allows.
//
// Why this file looks structurally different from main_windows.go:
// on Windows the whole process (including the tray UI) runs elevated
// via a Scheduled Task, so one process can both show the tray icon and
// create the network interface. On macOS that is not possible: a
// process running as root cannot attach to the logged-in user's
// WindowServer session (no menu bar item), and a process attached to
// the WindowServer session cannot be root. So this process ALWAYS runs
// as the normal user (that's what shows the tray icon) and, only when
// it actually needs to create the tun interface, launches the existing
// govpn-client CLI as a short-lived elevated background process via
// `osascript ... with administrator privileges` (the standard macOS
// admin-password prompt). The two processes don't share memory, so
// they talk through two small files instead of an in-process
// core.Engine: a status JSON file (written by the elevated CLI, polled
// by this GUI) and a pid file (so this GUI can tell the elevated
// process to stop).
//
// Practical difference from Windows the user should know about: this
// app cannot silently re-elevate forever in the background the way the
// Windows task does, because that would mean popping a macOS admin
// password dialog every retry (15s) — intrusive and easy to mistake
// for malware. Instead, after one cancelled/failed elevation this GUI
// stops auto-retrying and waits for "Reconectar agora" or a saved
// config.yml edit, logged in gui.log either way.
import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

	supervisor *macSupervisor
)

const reconnectRetryInterval = 15 * time.Second
const pollInterval = 2 * time.Second
const staleAfter = 20 * time.Second // no status-file heartbeat in this long => treat as disconnected

func main() {
	// Single-instance guard: an flock on our own lock file (Windows
	// uses a named Mutex; macOS/BSD has no exact equivalent reachable
	// from Go without cgo, flock on a regular file does the same job).
	dir, err := core.StateDir()
	if err != nil {
		dir = os.TempDir()
	}
	os.MkdirAll(dir, 0700)
	lockPath := filepath.Join(dir, "gui.lock")
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err == nil {
		if ferr := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); ferr != nil {
			// another instance already holds the lock
			return
		}
		// deliberately never closed/unlocked: held for the whole
		// process lifetime, released automatically on exit/crash
	}

	supervisor = newMacSupervisor()
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(iconDisconnected)
	systray.SetTitle("")
	systray.SetTooltip("govpn - iniciando...")

	mStatus = systray.AddMenuItem("Iniciando...", "estado atual")
	mStatus.Disable()
	systray.AddSeparator()
	mReconnect = systray.AddMenuItem("Reconectar agora", "Relê a configuração e tenta conectar imediatamente (pode pedir sua senha de administrador)")
	mDisconnect = systray.AddMenuItem("Desconectar", "Para o túnel sem fechar o ícone (pede senha de administrador)")
	mDisconnect.Disable()
	mOpenConfig = systray.AddMenuItem("Abrir configuração", "Edita "+supervisor.configPath)
	mOpenLogs = systray.AddMenuItem("Abrir pasta de logs", "")
	systray.AddSeparator()
	mQuit = systray.AddMenuItem("Sair", "Para o túnel (se estiver rodando) e fecha o ícone")

	go supervisor.run()
	go uiPollLoop()
	go menuLoop()
}

func menuLoop() {
	for {
		select {
		case <-mReconnect.ClickedCh:
			supervisor.manualReconnect()
		case <-mDisconnect.ClickedCh:
			go supervisor.disconnect()
		case <-mOpenConfig.ClickedCh:
			supervisor.ensureConfigExists()
			exec.Command("open", "-a", "TextEdit", supervisor.configPath).Start()
		case <-mOpenLogs.ClickedCh:
			if dir, err := core.StateDir(); err == nil {
				os.MkdirAll(dir, 0700)
				exec.Command("open", dir).Start()
			}
		case <-mQuit.ClickedCh:
			supervisor.disconnect() // best-effort: prompts once more if something is running
			systray.Quit()
			return
		}
	}
}

// uiPollLoop refreshes the tray icon/tooltip/menu from supervisor
// state every couple seconds — same "poll, don't push" approach as the
// Windows client uses internally, just across a process boundary here.
func uiPollLoop() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		st := supervisor.uiState()
		if st.connected {
			systray.SetIcon(iconConnected)
			systray.SetTooltip("govpn - conectado (" + st.ip + ")")
			mStatus.SetTitle("Conectado: " + st.ip)
			mDisconnect.Enable()
		} else {
			systray.SetIcon(iconDisconnected)
			msg := st.message
			if msg == "" {
				msg = "Desconectado"
			}
			systray.SetTooltip("govpn - " + msg)
			mStatus.SetTitle(msg)
			mDisconnect.Disable()
		}
	}
}

func onExit() {}

// ---- supervisor: owns config/status/pid file paths and the elevated
// child process lifecycle ----

type uiState struct {
	connected bool
	ip        string
	message   string
}

type macSupervisor struct {
	configPath string
	stateDir   string
	statusPath string
	pidPath    string
	logPath    string
	binPath    string

	mu            sync.Mutex
	lastMsg       string
	cooldown      bool // true after a cancelled/failed elevation: stop auto-retrying
	lastConfigSig string
}

func newMacSupervisor() *macSupervisor {
	home, _ := os.UserHomeDir()
	stateDir, err := core.StateDir()
	if err != nil {
		stateDir = filepath.Join(home, ".govpn")
	}
	return &macSupervisor{
		configPath: filepath.Join(home, "Library", "Application Support", "govpn", "config.yml"),
		stateDir:   stateDir,
		statusPath: filepath.Join(stateDir, "gui_status.json"),
		pidPath:    filepath.Join(stateDir, "gui_child.pid"),
		logPath:    filepath.Join(stateDir, "gui_child.log"),
		binPath:    siblingBinary("govpn-client"),
	}
}

// siblingBinary resolves a binary installed next to this one (same
// Contents/MacOS/ inside the .app bundle), falling back to PATH.
func siblingBinary(name string) string {
	if exe, err := os.Executable(); err == nil {
		if real, err2 := filepath.EvalSymlinks(exe); err2 == nil {
			exe = real
		}
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return name // let exec.Command fall back to PATH
}

// run is the background supervision loop: tries to get connected,
// backs off from re-prompting after a cancelled/failed elevation, and
// wakes back up if the user edits config.yml.
func (s *macSupervisor) run() {
	s.ensureConfigExists()
	os.MkdirAll(s.stateDir, 0700)
	for {
		if s.childAlive() {
			time.Sleep(reconnectRetryInterval)
			continue
		}
		s.mu.Lock()
		cd := s.cooldown
		s.mu.Unlock()
		sig := s.configSignature()
		if cd {
			// Only wake up again if the config actually changed
			// since the last (failed/cancelled) attempt, or the
			// user clicked "Reconectar agora" (which clears the
			// cooldown directly).
			s.mu.Lock()
			changed := sig != s.lastConfigSig
			s.mu.Unlock()
			if !changed {
				time.Sleep(pollInterval)
				continue
			}
			s.mu.Lock()
			s.cooldown = false
			s.mu.Unlock()
		}
		s.attemptConnect()
		time.Sleep(reconnectRetryInterval)
	}
}

func (s *macSupervisor) manualReconnect() {
	s.mu.Lock()
	s.cooldown = false
	s.mu.Unlock()
	go s.attemptConnect()
}

func (s *macSupervisor) configSignature() string {
	fi, err := os.Stat(s.configPath)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

// attemptConnect launches the CLI as an elevated background process.
// This is the ONE action that pops the macOS admin-password dialog.
func (s *macSupervisor) attemptConnect() {
	s.mu.Lock()
	s.lastConfigSig = s.configSignature()
	s.mu.Unlock()

	if _, err := os.Stat(s.configPath); err != nil {
		s.setMessage("Aguardando configuração (" + s.configPath + ")")
		return
	}

	// Explicit GOVPN_STATE_DIR: the elevated child runs as root, whose
	// own home dir is NOT this user's — without this its state.json
	// (device identity) would land under /var/root instead of here,
	// breaking "reuse saved profile" on every future run and leaving
	// an orphaned registration per attempt.
	script := fmt.Sprintf(
		"export GOVPN_STATE_DIR=%s; nohup %s up --config %s --status-file %s > %s 2>&1 & echo $!",
		shQuote(s.stateDir), shQuote(s.binPath), shQuote(s.configPath), shQuote(s.statusPath), shQuote(s.logPath),
	)
	out, err := runElevatedShell(script)
	if err != nil {
		if isUserCancelled(err) {
			s.setMessage("Elevação cancelada — clique em \"Reconectar agora\" quando quiser tentar de novo")
			s.mu.Lock()
			s.cooldown = true
			s.mu.Unlock()
			return
		}
		s.setMessage("Erro ao elevar: " + err.Error())
		return
	}
	pid := strings.TrimSpace(out)
	if pid != "" {
		os.WriteFile(s.pidPath, []byte(pid), 0600)
	}
	s.setMessage("Conectando...")
}

// disconnect stops the elevated child, if any (also prompts once).
func (s *macSupervisor) disconnect() {
	pid := s.readPID()
	if pid == "" {
		return
	}
	script := fmt.Sprintf("kill %s", shQuote(pid))
	runElevatedShell(script) // best-effort; ignore errors (already dead, etc.)
	os.Remove(s.pidPath)
	writeDisconnectedStatus(s.statusPath)
}

func (s *macSupervisor) childAlive() bool {
	pid := s.readPID()
	if pid == "" {
		return false
	}
	return pidAlive(pid)
}

func (s *macSupervisor) readPID() string {
	b, err := os.ReadFile(s.pidPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s *macSupervisor) setMessage(msg string) {
	s.mu.Lock()
	s.lastMsg = msg
	s.mu.Unlock()
}

// uiState merges the last status-file snapshot with whatever message
// the supervisor wants shown (config missing, elevation cancelled...).
func (s *macSupervisor) uiState() uiState {
	s.mu.Lock()
	msg := s.lastMsg
	s.mu.Unlock()

	sf, err := readStatusFile(s.statusPath)
	if err != nil {
		return uiState{connected: false, message: msg}
	}
	if time.Since(sf.UpdatedAt) > staleAfter {
		return uiState{connected: false, message: "Sem resposta do processo (pode ter travado)"}
	}
	if sf.LastError != "" && !sf.Connected {
		return uiState{connected: false, message: "Erro: " + sf.LastError}
	}
	if sf.Connected {
		return uiState{connected: true, ip: sf.AssignedIP}
	}
	return uiState{connected: false, message: msg}
}

// ensureConfigExists writes a starter config.yml (same shape/fields as
// Linux/Windows) if nothing is there yet, so "Abrir configuração"
// always has something to edit.
func (s *macSupervisor) ensureConfigExists() {
	if _, err := os.Stat(s.configPath); err == nil {
		return
	}
	os.MkdirAll(filepath.Dir(s.configPath), 0700)
	hn, _ := os.Hostname()
	content := "server: \"http://SEU_SERVIDOR:28443\"\n" +
		"authkey: \"SUA_AUTH_KEY\"\n" +
		"hostname: \"" + hn + "\"\n" +
		"iface: \"utun\"\n" +
		"poll_interval: \"10s\"\n"
	os.WriteFile(s.configPath, []byte(content), 0600)
}

// ---- small helpers ----

type childStatus struct {
	Connected  bool      `json:"connected"`
	AssignedIP string    `json:"assigned_ip,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	PID        int       `json:"pid"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func readStatusFile(path string) (*childStatus, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s childStatus
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func writeDisconnectedStatus(path string) {
	s := childStatus{Connected: false, UpdatedAt: time.Now()}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path, b, 0644)
}

// pidAlive checks liveness without needing root: sending signal 0 to a
// process you don't own still distinguishes "exists" (EPERM) from
// "doesn't exist" (ESRCH) — exactly what we need since the child runs
// as root and this GUI does not.
func pidAlive(pidStr string) bool {
	var pid int
	if _, err := fmt.Sscanf(pidStr, "%d", &pid); err != nil {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil || err == syscall.EPERM {
		return true
	}
	return false
}

// runElevatedShell pops the standard macOS "<app> wants to make
// changes" admin-password dialog and runs script as root via
// /bin/sh -c. Returns script's stdout (we use it to capture the
// backgrounded child's PID via `echo $!`).
func runElevatedShell(script string) (string, error) {
	osaScript := fmt.Sprintf("do shell script %s with administrator privileges", appleScriptQuote(script))
	out, err := exec.Command("osascript", "-e", osaScript).CombinedOutput()
	return string(out), err
}

func isUserCancelled(err error) bool {
	return strings.Contains(err.Error(), "-128") || strings.Contains(strings.ToLower(err.Error()), "user canceled")
}

// shQuote single-quotes a value for safe embedding in the /bin/sh
// script string (paths here are config-controlled, but quoting is
// cheap insurance against spaces/special chars in usernames).
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleScriptQuote wraps a string as an AppleScript string literal.
func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
