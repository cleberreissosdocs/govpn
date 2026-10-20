package core

import (
	"encoding/json"
	"os"
	"path/filepath"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// State is persisted locally after the first successful registration so
// subsequent runs don't need the auth key again.
type State struct {
	PrivateKey  string `json:"private_key"` // base64
	PublicKey   string `json:"public_key"`  // base64
	ServerURL   string `json:"server_url"`
	DeviceToken string `json:"device_token"`
	AssignedIP  string `json:"assigned_ip"`
	SubnetCIDR  string `json:"subnet_cidr"`
	Hostname    string `json:"hostname"`
	InsecureTLS bool   `json:"insecure_tls"` // skip TLS cert verification (self-signed server cert)
}

func StateDir() (string, error) {
	// keep this self-contained (no extra deps): honor GOVPN_STATE_DIR,
	// else use a sensible per-OS default.
	if d := os.Getenv("GOVPN_STATE_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".govpn"), nil
}

func StatePath() (string, error) {
	d, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "state.json"), nil
}

func LoadState() (*State, error) {
	p, err := StatePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func SaveState(s *State) error {
	d, err := StateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0700); err != nil {
		return err
	}
	p, err := StatePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0600)
}

func GenerateKeyPair() (priv, pub string, err error) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return "", "", err
	}
	return key.String(), key.PublicKey().String(), nil
}
