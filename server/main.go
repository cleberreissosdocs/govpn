package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
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
	onlineThreshold := fs.Duration("online-threshold", 30*time.Second, "how long after the last beacon a device is still shown online in the admin panel")
	fs.Parse(args)

	store, err := OpenStore(*dbPath, *subnet)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	holder := NewStoreHolder(store, *subnet)
	holder.StartBackupScheduler()
	api := &API{store: holder, subnetCIDR: *subnet}

	pass := *adminPass
	if pass == "" {
		pass = randomPassword()
		log.Printf("admin panel password auto-generated (save it!): user=%s pass=%s", *adminUser, pass)
	}
	admin := NewAdmin(holder, *adminUser, pass, *onlineThreshold)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/register", api.handleRegister)
	mux.HandleFunc("/api/peers", api.handlePeers)
	mux.HandleFunc("/api/beacon", api.handleBeacon)
	admin.RegisterRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})

	log.Printf("govpn-server listening on %s (subnet %s, db %s, admin panel at /admin)", *addr, *subnet, *dbPath)
	log.Fatal(http.ListenAndServe(*addr, mux))
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
