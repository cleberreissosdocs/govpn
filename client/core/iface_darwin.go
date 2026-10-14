//go:build darwin

package core

import (
	"fmt"
	"net"
	"os/exec"
)

// On macOS the WireGuard TUN device is a kernel-native utun interface
// (no extra driver needed, unlike Windows' wintun.dll). The OS assigns
// the interface name itself — you request the generic "utun" and the
// kernel hands back whatever number is free (utun3, utun4, ...); you
// cannot pick an arbitrary name like "govpn0" the way Linux allows.
func DefaultIfaceName() string { return "utun" }

// ConfigureInterface assigns the device's mesh IP and brings the
// interface up via ifconfig, then adds a route for the whole mesh
// subnet through it (macOS doesn't auto-create that route for a
// point-to-point utun interface the way Linux does for a normal
// addr-with-prefix assignment). Requires root, same as the official
// WireGuard macOS app.
func ConfigureInterface(ifaceName, ip, subnetCIDR string) error {
	if _, _, err := net.ParseCIDR(subnetCIDR); err != nil {
		return fmt.Errorf("parse subnet: %w", err)
	}

	// utun is point-to-point: "ifconfig utunN inet <addr> <addr> up"
	// (source and destination the same is the common convention used
	// by wireguard-go/wg-quick on macOS for this kind of interface).
	if err := run("ifconfig", ifaceName, "inet", ip, ip, "up"); err != nil {
		return fmt.Errorf("ifconfig: %w", err)
	}
	if err := run("route", "-q", "-n", "add", "-inet", subnetCIDR, "-interface", ifaceName); err != nil {
		return fmt.Errorf("route add: %w", err)
	}
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, string(out))
	}
	return nil
}
