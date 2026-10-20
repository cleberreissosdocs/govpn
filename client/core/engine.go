package core

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// Engine wraps the full client lifecycle (load/register state, bring the
// WireGuard interface up, keep peers in sync) behind a small Connect/
// Disconnect API so both the CLI and the GUI client can share one
// implementation.
type Engine struct {
	IfaceName     string
	PollInterval  time.Duration
	InsecureTLS   bool         // skip TLS cert verification when talking to the server (self-signed cert)
	OnLog         func(string) // optional; defaults to log.Printf if nil
	OnStateChange func(connected bool)

	mu      sync.Mutex
	state   *State
	wg      *WGManager
	sc      *ServerClient
	stopCh  chan struct{}
	running bool
}

func NewEngine(ifaceName string, pollInterval time.Duration) *Engine {
	return &Engine{IfaceName: ifaceName, PollInterval: pollInterval}
}

// UpgradeInsecureTLS turns insecure TLS verification ON (and persists
// it to the saved profile, if one is already loaded) — it never turns
// it off, matching the same "upgrade-only" safety rule as
// LoadOrRegister. Deprecated in favor of ApplyConfigOverrides, kept
// for any external caller still using it.
func (e *Engine) UpgradeInsecureTLS(v bool) error {
	return e.ApplyConfigOverrides("", v)
}

// ApplyConfigOverrides re-applies a subset of config.yml fields onto an
// ALREADY-REGISTERED profile, persisting the change. This matters for
// clients (like the Windows tray app) that only consult config.yml
// once, at first registration — without this, editing config.yml
// after the device is already registered (e.g. flipping the server
// from http:// to https://, or turning insecure_tls on once the
// operator enables TLS on the server) would silently have no effect
// until the device deletes its saved state and re-registers from
// scratch, which is surprising and easy to get stuck on.
//
// serverURL, if non-empty and different from the saved one, always
// wins (same as LoadOrRegister's existing-profile path). insecureTLS
// only ever turns the setting ON, never off, to avoid a silent
// security downgrade from a stale/blank config value.
func (e *Engine) ApplyConfigOverrides(serverURL string, insecureTLS bool) error {
	e.mu.Lock()
	st := e.state
	if st == nil {
		// Not registered yet: just stage insecureTLS for the upcoming
		// LoadOrRegister/RegisterNew call (server URL is passed
		// directly to those instead).
		if insecureTLS {
			e.InsecureTLS = true
		}
		e.mu.Unlock()
		return nil
	}
	changed := false
	if serverURL != "" && st.ServerURL != serverURL {
		st.ServerURL = serverURL
		changed = true
	}
	if insecureTLS && !st.InsecureTLS {
		st.InsecureTLS = true
		changed = true
	}
	e.InsecureTLS = st.InsecureTLS
	e.mu.Unlock()
	if !changed {
		return nil
	}
	return SaveState(st)
}

func (e *Engine) logf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if e.OnLog != nil {
		e.OnLog(msg)
	} else {
		log.Print(msg)
	}
}

// LoadOrRegister loads the saved local state if present; otherwise it
// registers a brand-new device with the given server/authKey/hostname
// and persists the result. Safe to call before Connect().
func (e *Engine) LoadOrRegister(server, authKey, hostname string) error {
	st, err := LoadState()
	if err == nil {
		changed := false
		if server != "" && st.ServerURL != server {
			st.ServerURL = server // allow overriding the server on an existing profile
			changed = true
		}
		// e.InsecureTLS at this point reflects whatever the caller just
		// configured (CLI flag / config.yml). We only ever use that to
		// turn insecure mode ON for an existing profile (and persist
		// it) — never silently turn it back OFF just because this run
		// didn't repeat the flag, which would be a surprising security
		// downgrade. To go back to strict verification, remove the
		// saved state and re-register.
		if e.InsecureTLS && !st.InsecureTLS {
			st.InsecureTLS = true
			changed = true
		} else {
			e.InsecureTLS = st.InsecureTLS
		}
		if changed {
			if err := SaveState(st); err != nil {
				return fmt.Errorf("save updated state: %w", err)
			}
		}
		e.mu.Lock()
		e.state = st
		e.mu.Unlock()
		return nil
	}
	if server == "" || authKey == "" {
		return fmt.Errorf("nenhum perfil salvo: servidor e auth key são obrigatórios no primeiro uso")
	}
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate key pair: %w", err)
	}
	hn := hostname
	if hn == "" {
		hn, _ = os.Hostname()
	}
	sc := NewServerClient(server, "", e.InsecureTLS)
	reg, err := sc.Register(authKey, pub, hn)
	if err != nil {
		return fmt.Errorf("register with server: %w", err)
	}
	st = &State{
		PrivateKey:  priv,
		PublicKey:   pub,
		ServerURL:   server,
		DeviceToken: reg.DeviceToken,
		AssignedIP:  reg.AssignedIP,
		SubnetCIDR:  reg.SubnetCIDR,
		Hostname:    hn,
		InsecureTLS: e.InsecureTLS,
	}
	if err := SaveState(st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	e.mu.Lock()
	e.state = st
	e.mu.Unlock()
	return nil
}

// TryLoadSaved loads a previously saved profile, if any, without
// registering a new one. Returns false if there is none yet.
func (e *Engine) TryLoadSaved() bool {
	st, err := LoadState()
	if err != nil {
		return false
	}
	e.InsecureTLS = st.InsecureTLS
	e.mu.Lock()
	e.state = st
	e.mu.Unlock()
	return true
}

// RegisterNew always creates a brand-new keypair and registers it with
// the given server/authKey/hostname, overwriting any previously saved
// profile. Use this from an explicit "configure profile" action; use
// LoadOrRegister for the "resume what's already there" CLI path.
func (e *Engine) RegisterNew(server, authKey, hostname string) error {
	if server == "" || authKey == "" {
		return fmt.Errorf("servidor e auth key são obrigatórios")
	}
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate key pair: %w", err)
	}
	hn := hostname
	if hn == "" {
		hn, _ = os.Hostname()
	}
	sc := NewServerClient(server, "", e.InsecureTLS)
	reg, err := sc.Register(authKey, pub, hn)
	if err != nil {
		return fmt.Errorf("register with server: %w", err)
	}
	st := &State{
		PrivateKey:  priv,
		PublicKey:   pub,
		ServerURL:   server,
		DeviceToken: reg.DeviceToken,
		AssignedIP:  reg.AssignedIP,
		SubnetCIDR:  reg.SubnetCIDR,
		Hostname:    hn,
		InsecureTLS: e.InsecureTLS,
	}
	if err := SaveState(st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	e.mu.Lock()
	e.state = st
	e.mu.Unlock()
	return nil
}

// HasProfile reports whether a local profile is already saved/loaded.
func (e *Engine) HasProfile() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state != nil
}

// State returns a copy of the currently loaded profile, if any.
func (e *Engine) State() *State {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == nil {
		return nil
	}
	cp := *e.state
	return &cp
}

// IsRunning reports whether the WireGuard interface is currently up.
func (e *Engine) IsRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// Connect brings the WireGuard interface up and starts the background
// peer-sync loop. Requires LoadOrRegister to have been called first.
func (e *Engine) Connect() error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return nil
	}
	st := e.state
	e.mu.Unlock()
	if st == nil {
		return fmt.Errorf("nenhum perfil configurado")
	}

	relay := NewRelayClient(st.ServerURL, st.DeviceToken, e.InsecureTLS, func(s string) { e.logf("%s", s) })
	relay.Start()

	wg, err := NewWGManager(e.IfaceName, st.PrivateKey, 0, relay)
	if err != nil {
		relay.Stop()
		return fmt.Errorf("start wireguard device: %w", err)
	}
	if err := ConfigureInterface(wg.IfaceName, st.AssignedIP, st.SubnetCIDR); err != nil {
		wg.Close()
		return fmt.Errorf("configure interface %s: %w", wg.IfaceName, err)
	}
	e.logf("interface %s up with ip %s", wg.IfaceName, st.AssignedIP)

	sc := NewServerClient(st.ServerURL, st.DeviceToken, e.InsecureTLS)
	listenPort, err := wg.ListenPort()
	if err != nil {
		e.logf("warning: could not read listen port: %v", err)
	}

	stopCh := make(chan struct{})
	e.mu.Lock()
	e.wg = wg
	e.sc = sc
	e.stopCh = stopCh
	e.running = true
	e.mu.Unlock()
	if e.OnStateChange != nil {
		e.OnStateChange(true)
	}

	syncOnce := func() {
		if err := sc.Beacon(listenPort); err != nil {
			e.logf("beacon failed: %v", err)
		}
		plist, err := sc.Peers()
		if err != nil {
			e.logf("fetch peers failed: %v", err)
			return
		}
		if err := wg.ApplyPeers(plist.Peers); err != nil {
			e.logf("apply peers failed: %v", err)
			return
		}
		e.logf("synced %d peer(s)", len(plist.Peers))
	}

	go func() {
		syncOnce()
		ticker := time.NewTicker(e.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				syncOnce()
			}
		}
	}()
	return nil
}

// Disconnect stops the sync loop and tears down the WireGuard interface.
func (e *Engine) Disconnect() error {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return nil
	}
	close(e.stopCh)
	wg := e.wg
	e.running = false
	e.wg = nil
	e.sc = nil
	e.mu.Unlock()

	if wg != nil {
		wg.Close()
	}
	e.logf("desconectado")
	if e.OnStateChange != nil {
		e.OnStateChange(false)
	}
	return nil
}
