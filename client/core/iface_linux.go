//go:build linux

package core

import (
	"fmt"
	"net"
	"os/exec"
)

// ConfigureInterface assigns the device's mesh IP (with the full mesh
// subnet mask, so the kernel routes the whole mesh range through this
// interface) and brings it up.
func ConfigureInterface(ifaceName, ip, subnetCIDR string) error {
	_, ipnet, err := net.ParseCIDR(subnetCIDR)
	if err != nil {
		return fmt.Errorf("parse subnet: %w", err)
	}
	ones, _ := ipnet.Mask.Size()
	addrCIDR := fmt.Sprintf("%s/%d", ip, ones)

	if err := run("ip", "addr", "add", addrCIDR, "dev", ifaceName); err != nil {
		return fmt.Errorf("ip addr add: %w", err)
	}
	if err := run("ip", "link", "set", "mtu", fmt.Sprintf("%d", wgMTU), "up", "dev", ifaceName); err != nil {
		return fmt.Errorf("ip link set up: %w", err)
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
