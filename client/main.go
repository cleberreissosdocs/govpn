package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"govpn/client/core"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "up":
		cmdUp(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "daemon":
		cmdDaemon(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`govpn-client - mesh VPN client agent

Usage:
  govpn-client up --server https://host:8443 --authkey KEY --hostname NAME
      First run: registers with the server, creates the WireGuard
      interface and keeps it in sync with the mesh. Subsequent runs
      reuse the saved state (~/.govpn/state.json) and the flags above
      are optional.

  govpn-client up --config /etc/govpn/config.yml
      Same thing, reading server/authkey/hostname/iface/poll_interval
      from a YAML file instead of flags — handy for systemd/unattended
      setups. Any flag passed alongside --config still overrides the
      matching field in the file.

  govpn-client status
      Prints the locally saved state (assigned IP, server, etc).

  govpn-client daemon --config /path/config.yml --status-file F --cmd-dir D
      Long-running supervisor mode used by the macOS LaunchDaemon:
      keeps retrying forever (same as systemd on Linux) instead of
      exiting on the first error, reloads config.yml on edit, and
      accepts "reconnect"/"disconnect" requests via touch-files in
      --cmd-dir — all so a privileged root service can run once at
      boot and never has to ask for a password again (see
      client-gui/main_darwin.go for the full design).

For a graphical, system-tray client on Windows and macOS, use
govpn-client-gui(.exe) instead (same profile file, same mesh).
`)
}

// statusFile persists a small JSON file describing the live
// connection state — used by the macOS tray app (client-gui), which
// is unprivileged and reads this instead of embedding its own
// core.Engine: the actual VPN work runs as a separate root
// LaunchDaemon process (see cmdDaemon), set up once at install time.
// Both cmdUp (Linux/manual) and cmdDaemon (macOS service) write it;
// only the macOS GUI polls it. Not used by the Windows GUI, which
// embeds its own Engine directly in the same (already privileged)
// process.
type statusFile struct {
	Connected  bool      `json:"connected"`
	AssignedIP string    `json:"assigned_ip,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	PID        int       `json:"pid"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func writeStatusFile(path string, connected bool, ip, lastErr string) {
	if path == "" {
		return
	}
	sf := statusFile{Connected: connected, AssignedIP: ip, LastError: lastErr, PID: os.Getpid(), UpdatedAt: time.Now()}
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return
	}
	os.Rename(tmp, path) // atomic-ish: never leaves a half-written file for the GUI to read
}

func cmdStatus(args []string) {
	s, err := core.LoadState()
	if err != nil {
		fmt.Println("no local state yet - run 'govpn-client up' first")
		return
	}
	fmt.Printf("server:      %s\n", s.ServerURL)
	fmt.Printf("hostname:    %s\n", s.Hostname)
	fmt.Printf("assigned ip: %s\n", s.AssignedIP)
	fmt.Printf("subnet:      %s\n", s.SubnetCIDR)
	fmt.Printf("public key:  %s\n", s.PublicKey)
}

func cmdUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	configPath := fs.String("config", "", "path to a YAML config file (server/authkey/hostname/iface/poll_interval) — flags above still override it")
	server := fs.String("server", "", "coordination server base URL, e.g. https://1.2.3.4:8443")
	authKey := fs.String("authkey", "", "auth key (only needed on first registration)")
	hostname := fs.String("hostname", "", "this device's name shown to peers")
	iface := fs.String("iface", core.DefaultIfaceName(), "tun interface name")
	pollInterval := fs.Duration("poll", 10*time.Second, "how often to refresh the peer list")
	insecure := fs.Bool("insecure", false, "skip TLS certificate verification (needed for the server's default self-signed cert; only safe on a trusted network — prefer a real cert on the server instead)")
	statusFilePath := fs.String("status-file", "", "write live connection status as JSON to this path on every change (polled by client-gui on macOS — see type statusFile)")
	fs.Parse(args)

	// Track which flags the user actually passed on the command line, so
	// an explicit flag always wins over the config file, and the config
	// file always wins over the flag's zero-value default.
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	if *configPath != "" {
		cfg, err := core.LoadFileConfig(*configPath)
		if err != nil {
			log.Fatal(err)
		}
		if cfg != nil {
			if !explicit["server"] && cfg.Server != "" {
				*server = cfg.Server
			}
			if !explicit["authkey"] && cfg.AuthKey != "" {
				*authKey = cfg.AuthKey
			}
			if !explicit["hostname"] && cfg.Hostname != "" {
				*hostname = cfg.Hostname
			}
			if !explicit["iface"] && cfg.Iface != "" {
				*iface = cfg.Iface
			}
			if !explicit["poll"] {
				*pollInterval = core.ParsePollInterval(cfg.PollInterval, *pollInterval)
			}
			if !explicit["insecure"] && cfg.InsecureTLS {
				*insecure = true
			}
		}
	}

	eng := core.NewEngine(*iface, *pollInterval)
	eng.InsecureTLS = *insecure
	eng.OnStateChange = func(connected bool) {
		ip := ""
		if st := eng.State(); st != nil {
			ip = st.AssignedIP
		}
		writeStatusFile(*statusFilePath, connected, ip, "")
	}
	if err := eng.LoadOrRegister(*server, *authKey, *hostname); err != nil {
		writeStatusFile(*statusFilePath, false, "", err.Error())
		log.Fatal(err)
	}
	st := eng.State()
	log.Printf("profile ready: assigned_ip=%s subnet=%s server=%s", st.AssignedIP, st.SubnetCIDR, st.ServerURL)

	if err := eng.Connect(); err != nil {
		writeStatusFile(*statusFilePath, false, "", err.Error())
		log.Fatalf("connect: %v", err)
	}
	defer eng.Disconnect()

	// Heartbeat: refresh updated_at periodically even without a state
	// change, so a poller (client-gui) can tell "still alive and
	// connected" apart from "process died without a chance to write
	// disconnected" (e.g. killed, crashed, machine slept).
	if *statusFilePath != "" {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				ip := ""
				if st := eng.State(); st != nil {
					ip = st.AssignedIP
				}
				writeStatusFile(*statusFilePath, eng.IsRunning(), ip, "")
			}
		}()
	}

	select {} // run forever; Ctrl-C / signal kills the process
}

// cmdDaemon is the long-running, self-healing supervisor used by the
// macOS LaunchDaemon (root, started once at boot via the .pkg
// installer). Unlike cmdUp, it never gives up after one failure: it
// keeps retrying (same spirit as the Linux systemd unit's
// Restart=on-failure, just handled inside the process instead of by
// the service manager) and reloads config.yml on edit — which is what
// lets the unprivileged tray app avoid ever asking for a password
// again after install: there's no per-connect elevation anymore, the
// privileged work already happened once, at boot.
func cmdDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yml (required)")
	statusFilePath := fs.String("status-file", "", "write live connection status as JSON here")
	cmdDir := fs.String("cmd-dir", "", "directory polled for reconnect/disconnect touch-files from the tray app")
	fs.Parse(args)

	if *configPath == "" {
		log.Fatal("daemon: --config is required")
	}

	eng := core.NewEngine(core.DefaultIfaceName(), 10*time.Second)
	eng.OnStateChange = func(connected bool) {
		ip := ""
		if st := eng.State(); st != nil {
			ip = st.AssignedIP
		}
		writeStatusFile(*statusFilePath, connected, ip, "")
	}
	eng.TryLoadSaved() // so HasProfile() is accurate from the first attempt(), not just after one successful LoadOrRegister

	var mu sync.Mutex
	manualStop := false
	lastConfigSig := ""
	var lastReconnectSeen, lastDisconnectSeen time.Time

	configSig := func() string {
		fi, err := os.Stat(*configPath)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
	}

	attempt := func() {
		cfg, err := core.LoadFileConfig(*configPath)
		if err != nil || cfg == nil {
			writeStatusFile(*statusFilePath, false, "", "config inválido ou não encontrado: "+*configPath)
			return
		}
		if !eng.HasProfile() && (cfg.Server == "" || cfg.AuthKey == "") {
			writeStatusFile(*statusFilePath, false, "", "preencha server e authkey em "+*configPath)
			return
		}
		if cfg.Iface != "" {
			eng.IfaceName = cfg.Iface
		}
		if d := core.ParsePollInterval(cfg.PollInterval, 0); d > 0 {
			eng.PollInterval = d
		}
		eng.InsecureTLS = cfg.InsecureTLS
		mu.Lock()
		lastConfigSig = configSig()
		mu.Unlock()
		if err := eng.LoadOrRegister(cfg.Server, cfg.AuthKey, cfg.Hostname); err != nil {
			writeStatusFile(*statusFilePath, false, "", err.Error())
			return
		}
		if err := eng.Connect(); err != nil {
			writeStatusFile(*statusFilePath, false, "", err.Error())
		}
	}

	// Supervisor loop: silent auto-retry every 15s, same cadence as
	// the Windows tray client's connectLoop — safe here because
	// there's no password dialog in the way anymore.
	go func() {
		for {
			mu.Lock()
			stopped := manualStop
			mu.Unlock()
			if !stopped && !eng.IsRunning() {
				attempt()
			}
			time.Sleep(15 * time.Second)
		}
	}()

	// Heartbeat, same purpose as in cmdUp.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ip := ""
			if st := eng.State(); st != nil {
				ip = st.AssignedIP
			}
			writeStatusFile(*statusFilePath, eng.IsRunning(), ip, "")
		}
	}()

	// Command + config-reload watcher: the tray app has no privilege
	// to signal this process directly, so it just touches files in
	// --cmd-dir and we poll for them — same "poll, don't push"
	// approach used throughout this project.
	if *cmdDir != "" {
		os.MkdirAll(*cmdDir, 0775)
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if fi, err := os.Stat(filepath.Join(*cmdDir, "reconnect")); err == nil && fi.ModTime().After(lastReconnectSeen) {
					lastReconnectSeen = fi.ModTime()
					mu.Lock()
					manualStop = false
					mu.Unlock()
					if eng.IsRunning() {
						eng.Disconnect()
					}
					go attempt()
				}
				if fi, err := os.Stat(filepath.Join(*cmdDir, "disconnect")); err == nil && fi.ModTime().After(lastDisconnectSeen) {
					lastDisconnectSeen = fi.ModTime()
					mu.Lock()
					manualStop = true
					mu.Unlock()
					eng.Disconnect()
				}
				// config.yml changed (saved from "Abrir configuração"):
				// wake up and retry immediately instead of waiting up
				// to 15s, and clear any previous manual-stop so fixing
				// the config after a "Desconectar" reconnects on its
				// own.
				mu.Lock()
				sig := lastConfigSig
				mu.Unlock()
				if cs := configSig(); cs != "" && cs != sig {
					mu.Lock()
					manualStop = false
					mu.Unlock()
					if !eng.IsRunning() {
						go attempt()
					}
				}
			}
		}()
	}

	select {} // run forever; the LaunchDaemon's KeepAlive restarts us if we ever exit
}
