package main

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// relayRegistry keeps one long-lived, hijacked connection per currently
// online device (keyed by its base64 WireGuard public key) and forwards
// opaque framed payloads between them. It exists purely as a fallback
// for when two peers can't establish a direct WireGuard UDP path (both
// behind NAT, no port forwarding) — see server/relay.go's handler and
// the matching client/core/relaybind.go for the framing and the
// "try direct AND relay, let WireGuard's own anti-replay dedupe
// whichever arrives" strategy that avoids needing failure detection.
//
// Wire format, identical in both directions once a connection is
// registered: [1 byte: pubkey_len][pubkey, base64][2 bytes BE:
// payload_len][payload]. From client->server the pubkey names the
// DESTINATION; from server->client it's been rewritten to name the
// SOURCE, so the receiver knows which peer to hand the packet to.
type relayRegistry struct {
	mu    sync.RWMutex
	conns map[string]net.Conn // pubkey (base64) -> hijacked conn
}

func newRelayRegistry() *relayRegistry {
	return &relayRegistry{conns: make(map[string]net.Conn)}
}

func (r *relayRegistry) register(pubKey string, conn net.Conn) {
	r.mu.Lock()
	// If this pubkey reconnects (new process, roamed network), the old
	// hijacked conn is now stale — drop it so writes to it stop
	// silently piling up forever.
	if old, ok := r.conns[pubKey]; ok {
		old.Close()
	}
	r.conns[pubKey] = conn
	r.mu.Unlock()
}

func (r *relayRegistry) unregister(pubKey string, conn net.Conn) {
	r.mu.Lock()
	if cur, ok := r.conns[pubKey]; ok && cur == conn {
		delete(r.conns, pubKey)
	}
	r.mu.Unlock()
}

func (r *relayRegistry) onlineCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.conns)
}

// forward writes a relay frame to destPubKey's connection, if it's
// currently online. Best-effort: drops silently (same as a dropped UDP
// packet would) if the peer isn't connected to the relay right now —
// callers have no business blocking the sender on a peer's liveness.
func (r *relayRegistry) forward(srcPubKey, destPubKey string, payload []byte) {
	r.mu.RLock()
	conn, ok := r.conns[destPubKey]
	r.mu.RUnlock()
	if !ok {
		return
	}
	frame := encodeRelayFrame(srcPubKey, payload)
	// One write() per frame keeps this atomic enough for our purposes
	// (no interleaving of two frames mid-write) without needing a
	// per-conn write mutex — net.Conn.Write for a TCP/TLS stream is
	// safe to call concurrently as long as each call's bytes land
	// together, which a single Write() of the whole frame guarantees.
	if _, err := conn.Write(frame); err != nil {
		// Let the device's own read loop notice the conn is dead and
		// unregister it; we just log once here for visibility.
		log.Printf("relay: write to %s failed: %v", destPubKey, err)
	}
}

func encodeRelayFrame(pubKey string, payload []byte) []byte {
	buf := make([]byte, 0, 1+len(pubKey)+2+len(payload))
	buf = append(buf, byte(len(pubKey)))
	buf = append(buf, pubKey...)
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(payload)))
	buf = append(buf, lenBuf[:]...)
	buf = append(buf, payload...)
	return buf
}

const maxRelayPayload = 2048 // generous over wg's ~1420 MTU + overhead

// handleRelay upgrades an authenticated device's HTTP connection into a
// long-lived bidirectional relay tunnel. Uses a bespoke 101-style
// upgrade (not real WebSocket) to avoid pulling in a dependency for
// something this simple — see the framing doc on relayRegistry.
func (a *API) handleRelay(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	pubKey, _, err := a.store.Get().DeviceByToken(token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "relay not supported by this server transport", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	// Hijacking opts us out of the server's own header writing, and
	// opts us OUT of any ReadTimeout/WriteTimeout it might have set on
	// this conn (reset them to no deadline, since this connection is
	// meant to live for as long as the client stays up).
	conn.SetDeadline(zeroTime)

	if _, err := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: govpn-relay\r\nConnection: Upgrade\r\n\r\n"); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}

	a.relay.register(pubKey, conn)
	defer a.relay.unregister(pubKey, conn)

	br := rw.Reader
	for {
		destLen, err := br.ReadByte()
		if err != nil {
			if err != io.EOF {
				log.Printf("relay: read from %s ended: %v", pubKey, err)
			}
			return
		}
		destBuf := make([]byte, destLen)
		if _, err := io.ReadFull(br, destBuf); err != nil {
			return
		}
		var payLen uint16
		if err := binary.Read(br, binary.BigEndian, &payLen); err != nil {
			return
		}
		if int(payLen) > maxRelayPayload {
			log.Printf("relay: %s sent an oversized frame (%d bytes), dropping connection", pubKey, payLen)
			return
		}
		payload := make([]byte, payLen)
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}
		a.relay.forward(pubKey, string(destBuf), payload)
	}
}

var zeroTime time.Time
