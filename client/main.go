package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
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

For a graphical, system-tray client on Windows and macOS, use
govpn-client-gui(.exe) instead (same profile file, same mesh).
`)
}

// statusWriter persists a small JSON file describing the live
// connection state — used by the macOS tray app (client-gui), which
// runs unprivileged and launches this CLI as a separate, elevated
// (root) background process to actually create the network interface.
// Since the two run as different users, they can't share in-process
// state (the GUI's core.Engine), so the elevated process writes its
// status here and the GUI polls the file instead. Not used by the
// Linux/Windows path, where the GUI embeds its own Engine directly in
// the same (already privileged) process.
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
	statusFilePath := fs.String("status-file", "", "write live connection status as JSON to this path on every change (used by client-gui on macOS, which runs this CLI as a separate elevated process and polls the file for status — see core.statusFile)")
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
