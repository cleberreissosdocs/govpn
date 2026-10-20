package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "genkey":
		cmdGenkey(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`govpn-server - coordination server for govpn

Usage:
  govpn-server serve  [--addr :8443] [--db ./govpn.db] [--subnet 100.64.0.0/10]
  govpn-server genkey [--db ./govpn.db] [--note "some note"] [--reusable]
`)
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8443", "listen address")
	dbPath := fs.String("db", "./govpn.db", "sqlite db path")
	subnet := fs.String("subnet", "100.64.0.0/10", "mesh subnet CIDR")
	adminUser := fs.String("admin-user", "admin", "admin panel username")
	adminPass := fs.String("admin-pass", "", "admin panel password (auto-generated and logged once if empty)")
	adminPassFile := fs.String("admin-pass-file", "", "path to a file containing the admin password — prefer this over --admin-pass, which is visible to any local user via `ps`/shell history")
	onlineThreshold := fs.Duration("online-threshold", 30*time.Second, "how long after the last beacon a device is still shown online in the admin panel")
	tlsCert := fs.String("tls-cert", "", "TLS certificate (PEM). If unset, a self-signed one is generated/reused next to --db")
	tlsKey := fs.String("tls-key", "", "TLS private key (PEM), paired with --tls-cert")
	tlsHosts := fs.String("tls-hosts", "", "comma-separated hostnames/IPs the self-signed cert should cover (e.g. your public IP/domain); ignored if --tls-cert is set")
	noTLS := fs.Bool("no-tls", false, "serve plain HTTP instead of HTTPS (NOT recommended: admin password, auth keys and device tokens would cross the network unencrypted — only use this behind a trusted reverse proxy that terminates TLS itself)")
	fs.Parse(args)

	store, err := OpenStore(*dbPath, *subnet)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	holder := NewStoreHolder(store, *subnet)
	holder.StartBackupScheduler()
	api := &API{store: holder, subnetCIDR: *subnet, relay: newRelayRegistry()}

	pass := *adminPass
	if *adminPassFile != "" {
		b, err := os.ReadFile(*adminPassFile)
		if err != nil {
			log.Fatalf("read admin-pass-file: %v", err)
		}
		pass = strings.TrimSpace(string(b))
	}
	if pass == "" {
		pass = randomPassword()
		log.Printf("admin panel password auto-generated (save it!): user=%s pass=%s", *adminUser, pass)
	}
	admin := NewAdmin(holder, *adminUser, pass, *onlineThreshold)
	admin.secureCookie = !*noTLS

	mux := http.NewServeMux()
	mux.HandleFunc("/api/register", api.handleRegister)
	mux.HandleFunc("/api/peers", api.handlePeers)
	mux.HandleFunc("/api/beacon", api.handleBeacon)
	mux.HandleFunc("/api/relay", api.handleRelay)
	admin.RegisterRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})

	handler := securityHeaders(mux, !*noTLS)

	if *noTLS {
		log.Printf("WARNING: --no-tls set, serving plain HTTP — admin password, auth keys and device tokens cross the network UNENCRYPTED. Only do this behind a TLS-terminating reverse proxy.")
		log.Printf("govpn-server listening on %s (HTTP, subnet %s, db %s, admin panel at /admin)", *addr, *subnet, *dbPath)
		log.Fatal(http.ListenAndServe(*addr, handler))
		return
	}

	certPath, keyPath := *tlsCert, *tlsKey
	if certPath == "" || keyPath == "" {
		dir := filepath.Dir(*dbPath)
		certPath = filepath.Join(dir, "govpn-selfsigned.crt")
		keyPath = filepath.Join(dir, "govpn-selfsigned.key")
		var hosts []string
		if *tlsHosts != "" {
			hosts = strings.Split(*tlsHosts, ",")
		}
		if err := ensureSelfSignedCert(certPath, keyPath, hosts); err != nil {
			log.Fatalf("generate self-signed cert: %v", err)
		}
		log.Printf("no --tls-cert given: using/generating a self-signed certificate at %s (clients must skip verification or pin it — pass --tls-hosts with your public IP/domain so the cert covers it, or supply a real --tls-cert/--tls-key)", certPath)
	}

	srv := &http.Server{
		Addr:      *addr,
		Handler:   handler,
		TLSConfig: tlsConfig(),
		// Disable HTTP/2: Go's net/http2 implementation doesn't
		// support http.Hijacker (needed by /api/relay's long-lived
		// tunnel — see server/relay.go). Without this, ALPN silently
		// negotiates h2 for any TLS client that offers it (which is
		// most modern HTTP clients, including Go's own net/http), and
		// hijacking just hangs forever instead of erroring cleanly.
		// Everything else (register/peers/beacon/admin panel) works
		// fine over plain HTTP/1.1, so there's no downside to this.
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
	log.Printf("govpn-server listening on %s (HTTPS, subnet %s, db %s, admin panel at /admin)", *addr, *subnet, *dbPath)
	log.Fatal(srv.ListenAndServeTLS(certPath, keyPath))
}

func cmdGenkey(args []string) {
	fs := flag.NewFlagSet("genkey", flag.ExitOnError)
	dbPath := fs.String("db", "./govpn.db", "sqlite db path")
	note := fs.String("note", "", "note describing this auth key")
	reusable := fs.Bool("reusable", false, "allow this key to register multiple devices")
	subnet := fs.String("subnet", "100.64.0.0/10", "mesh subnet CIDR (must match serve)")
	fs.Parse(args)

	store, err := OpenStore(*dbPath, *subnet)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	key, err := store.CreateAuthKey(*note, *reusable)
	if err != nil {
		log.Fatalf("create auth key: %v", err)
	}
	fmt.Println(key)
}
