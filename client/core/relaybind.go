package core

import (
	"net"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
)

// RelayBind wraps a real conn.Bind (normal UDP socket) and, for any
// peer we have a relay mapping for, ALSO ships a copy of every
// outgoing packet through the relay server. This "always try both"
// strategy is a deliberate simplification: WireGuard's own anti-replay
// window silently discards whichever copy of a packet arrives second,
// so we don't need any direct-path failure detection — if direct
// works, the relay copy is just harmless extra traffic; if it doesn't,
// the relay copy is what actually gets the handshake/data through.
//
// This trades some bandwidth (every packet relayed, not just the ones
// that need it) for a lot of implementation simplicity — a reasonable
// MVP tradeoff for small meshes. A future version could stop relaying
// once a direct handshake is confirmed recent.
type RelayBind struct {
	inner conn.Bind
	relay *RelayClient

	mu         sync.RWMutex
	pubkeyByEp map[string]string        // endpoint.DstToString() -> peer pubkey (base64)
	epByPubkey map[string]conn.Endpoint // peer pubkey (base64) -> last known endpoint object

	// stopCh is per-"generation": wireguard-go's device internally
	// calls Close() then Open() again every time the UDP listen port
	// is (re)applied via IpcSet — even a totally normal first-time
	// "listen_port=0" at startup goes through one such cycle. Close()
	// MUST unblock any in-flight ReceiveFunc calls (that's the Bind
	// interface's contract — device.Close() waits on a WaitGroup for
	// every receive goroutine to actually return), or the whole
	// device hangs forever on startup. So each Open() call gets its
	// own fresh channel, and Close() closes only the CURRENT one —
	// which unblocks that generation's relayReceiveFunc without
	// touching the relay connection itself (that's Shutdown()'s job,
	// called once by WGManager.Close() on the real, final teardown).
	stopMu sync.Mutex
	stopCh chan struct{}
}

var _ conn.Bind = (*RelayBind)(nil)

func NewRelayBind(inner conn.Bind, relay *RelayClient) *RelayBind {
	return &RelayBind{
		inner:      inner,
		relay:      relay,
		pubkeyByEp: make(map[string]string),
		epByPubkey: make(map[string]conn.Endpoint),
	}
}

// SetPeerEndpoint records which pubkey a given "ip:port" endpoint
// string belongs to, so Send() knows where to also relay outgoing
// packets and Receive() knows which conn.Endpoint to report for
// packets that arrive via the relay. Call this whenever a peer's
// endpoint is (re)configured (WGManager.ApplyPeers does, for every
// peer that has one).
func (b *RelayBind) SetPeerEndpoint(pubKey, epString string) {
	if epString == "" {
		return
	}
	ep, err := b.inner.ParseEndpoint(epString)
	if err != nil {
		return
	}
	dst := ep.DstToString()
	b.mu.Lock()
	b.pubkeyByEp[dst] = pubKey
	b.epByPubkey[pubKey] = ep
	b.mu.Unlock()
}

func (b *RelayBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actualPort, err := b.inner.Open(port)
	if err != nil {
		return nil, 0, err
	}
	if b.relay != nil {
		b.stopMu.Lock()
		stop := make(chan struct{})
		b.stopCh = stop
		b.stopMu.Unlock()
		fns = append(fns, b.relayReceiveFunc(stop))
	}
	return fns, actualPort, nil
}

func (b *RelayBind) relayReceiveFunc(stop <-chan struct{}) conn.ReceiveFunc {
	return func(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		select {
		case <-stop:
			return 0, net.ErrClosed
		case pkt, ok := <-b.relay.Recv():
			if !ok {
				return 0, net.ErrClosed
			}
			b.mu.RLock()
			ep, found := b.epByPubkey[pkt.SrcPubKey]
			b.mu.RUnlock()
			if !found {
				// Packet from a peer we don't have a configured
				// endpoint for yet (e.g. race with a peer list
				// update) — drop this one, caller will call us again.
				return 0, nil
			}
			n := copy(bufs[0], pkt.Payload)
			sizes[0] = n
			eps[0] = ep
			return 1, nil
		}
	}
}

func (b *RelayBind) Close() error {
	// Unblock THIS generation's relayReceiveFunc so wireguard-go's
	// internal WaitGroup.Wait() for all receive goroutines can
	// complete — required every time, including the very first
	// internal rebind at startup. Does NOT touch the relay connection
	// itself (see the stopCh doc comment above and Shutdown() below).
	b.stopMu.Lock()
	if b.stopCh != nil {
		close(b.stopCh)
		b.stopCh = nil
	}
	b.stopMu.Unlock()
	return b.inner.Close()
}

// Shutdown performs the actual, final teardown of the relay
// connection. Call this once, from WGManager.Close() — it's separate
// from Close() above (which wireguard-go also calls on every internal
// rebind, not just real teardown) specifically so the relay's TCP/TLS
// connection to the coordination server survives rebinds and only
// goes away when the engine is truly disconnecting.
func (b *RelayBind) Shutdown() {
	if b.relay != nil {
		b.relay.Stop()
	}
}

func (b *RelayBind) SetMark(mark uint32) error { return b.inner.SetMark(mark) }

func (b *RelayBind) BatchSize() int { return b.inner.BatchSize() }

func (b *RelayBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	return b.inner.ParseEndpoint(s)
}

func (b *RelayBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	directErr := b.inner.Send(bufs, ep)

	if b.relay != nil {
		dst := ep.DstToString()
		b.mu.RLock()
		pub, ok := b.pubkeyByEp[dst]
		b.mu.RUnlock()
		if ok {
			for _, buf := range bufs {
				cp := make([]byte, len(buf))
				copy(cp, buf)
				// Best-effort: ignore the error (logged inside
				// RelayClient already when the connection itself
				// drops); we must not let relay hiccups affect the
				// direct path's return value.
				_ = b.relay.SendTo(pub, cp)
			}
		}
	}

	return directErr
}
