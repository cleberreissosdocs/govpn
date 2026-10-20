package core

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// FileConfig is the optional on-disk config file (YAML) read by the CLI
// client, meant for unattended/systemd deployments where you don't want
// to bake the auth key into a unit file's ExecStart line.
type FileConfig struct {
	Server       string `yaml:"server"`
	AuthKey      string `yaml:"authkey"`
	Hostname     string `yaml:"hostname"`
	Iface        string `yaml:"iface"`
	PollInterval string `yaml:"poll_interval"`
	InsecureTLS  bool   `yaml:"insecure_tls"`
}

// LoadFileConfig reads and parses a YAML config file. A missing file is
// not an error (the caller should just fall back to flags/saved state);
// a malformed one is.
func LoadFileConfig(path string) (*FileConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg FileConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, nil
}

// ParsePollInterval parses the poll_interval string, falling back to a
// sane default on empty/invalid input rather than erroring out — a typo
// here shouldn't stop the VPN client from starting.
func ParsePollInterval(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
