package core

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"govpn/common"
)

type WGManager struct {
	dev       *device.Device
	tunDev    tun.Device
	IfaceName string
}

const wgMTU = 1420

func b64KeyToHex(b64 string) (string, error) {
	k, err := wgtypes.ParseKey(b64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(k[:]), nil
}

// NewWGManager creates the TUN device and the WireGuard engine, and sets
// the local private key + listen port.
func NewWGManager(ifaceNameHint, privKeyB64 string, listenPort int) (*WGManager, error) {
	tunDev, err := tun.CreateTUN(ifaceNameHint, wgMTU)
	if err != nil {
		return nil, fmt.Errorf("create tun: %w", err)
	}
	realName, err := tunDev.Name()
	if err != nil {
		realName = ifaceNameHint
	}

	logger := device.NewLogger(device.LogLevelError, "govpn: ")
	bind := conn.NewDefaultBind()
	dev := device.NewDevice(tunDev, bind, logger)

	privHex, err := b64KeyToHex(privKeyB64)
	if err != nil {
		return nil, fmt.Errorf("bad private key: %w", err)
	}
	cfg := fmt.Sprintf("private_key=%s\nlisten_port=%d\n", privHex, listenPort)
	if err := dev.IpcSet(cfg); err != nil {
		return nil, fmt.Errorf("ipc set base config: %w", err)
	}
	if err := dev.Up(); err != nil {
		return nil, fmt.Errorf("device up: %w", err)
	}

	return &WGManager{dev: dev, tunDev: tunDev, IfaceName: realName}, nil
}

// ListenPort reads back the UDP port the device actually bound to.
func (m *WGManager) ListenPort() (int, error) {
	out, err := m.dev.IpcGet()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "listen_port=") {
			v := strings.TrimPrefix(line, "listen_port=")
			return strconv.Atoi(v)
		}
	}
	return 0, fmt.Errorf("listen_port not found in device config")
}

// ApplyPeers replaces the full peer set on the device (full-mesh config).
func (m *WGManager) ApplyPeers(peers []common.Peer) error {
	var b strings.Builder
	b.WriteString("replace_peers=true\n")
	for _, p := range peers {
		pubHex, err := b64KeyToHex(p.PubKey)
		if err != nil {
			continue // skip malformed peer, don't kill the whole update
		}
		b.WriteString("public_key=" + pubHex + "\n")
		b.WriteString("replace_allowed_ips=true\n")
		b.WriteString("allowed_ip=" + p.IP + "/32\n")
		if p.Endpoint != "" {
			b.WriteString("endpoint=" + p.Endpoint + "\n")
		}
		b.WriteString("persistent_keepalive_interval=25\n")
	}
	return m.dev.IpcSet(b.String())
}

func (m *WGManager) Close() {
	m.dev.Close()
}
