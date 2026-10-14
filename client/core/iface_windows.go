//go:build windows

package core

import (
	"fmt"
	"net"
	"os/exec"
)

// ConfigureInterface assigns the device's mesh IP via netsh. Requires the
// process to run elevated (Administrator), same as the WireGuard client.
func ConfigureInterface(ifaceName, ip, subnetCIDR string) error {
	_, ipnet, err := net.ParseCIDR(subnetCIDR)
	if err != nil {
		return fmt.Errorf("parse subnet: %w", err)
	}
	mask := net.IP(ipnet.Mask).String()

	if err := run("netsh", "interface", "ip", "set", "address",
		"name="+ifaceName, "static", ip, mask); err != nil {
		return fmt.Errorf("netsh set address: %w", err)
	}
	if err := run("netsh", "interface", "set", "interface", ifaceName, "admin=enable"); err != nil {
		return fmt.Errorf("netsh enable interface: %w", err)
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
