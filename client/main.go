package main

import (
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

For a graphical, system-tray client on Windows, use govpn-client-gui.exe
instead (same profile file, same mesh).
`)
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
	if err := eng.LoadOrRegister(*server, *authKey, *hostname); err != nil {
		log.Fatal(err)
	}
	st := eng.State()
	log.Printf("profile ready: assigned_ip=%s subnet=%s server=%s", st.AssignedIP, st.SubnetCIDR, st.ServerURL)

	if err := eng.Connect(); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer eng.Disconnect()

	select {} // run forever; Ctrl-C / signal kills the process
}
