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
	server := fs.String("server", "", "coordination server base URL, e.g. https://1.2.3.4:8443")
	authKey := fs.String("authkey", "", "auth key (only needed on first registration)")
	hostname := fs.String("hostname", "", "this device's name shown to peers")
	iface := fs.String("iface", "govpn0", "tun interface name")
	pollInterval := fs.Duration("poll", 10*time.Second, "how often to refresh the peer list")
	fs.Parse(args)

	eng := core.NewEngine(*iface, *pollInterval)
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
