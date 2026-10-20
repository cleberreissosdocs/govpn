package core

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// RelayPacket is a payload that arrived via the relay instead of a
// direct UDP path, tagged with which peer (by base64 pubkey) sent it.
type RelayPacket struct {
	SrcPubKey string
	Payload   []byte
}

const maxRelayPayload = 2048

// RelayClient maintains a single long-lived connection to the
// coordination server's /api/relay endpoint and uses it to ferry raw
// WireGuard packets to/from peers when a direct UDP path isn't
// working (both ends behind NAT, no port forwarding, no STUN-assisted
// hole punch implemented). It reconnects with a fixed backoff and is
// entirely best-effort: if it can't connect, direct-only WireGuard
// traffic keeps working exactly as before — this is purely additive.
type RelayClient struct {
	serverURL   string
	token       string
	insecureTLS bool
	onLog       func(string)

	recvCh chan RelayPacket

	mu     sync.Mutex
	conn   net.Conn
	writer *bufio.Writer
	closed bool
}

func NewRelayClient(serverURL, token string, insecureTLS bool, onLog func(string)) *RelayClient {
	return &RelayClient{
		serverURL:   strings.TrimRight(serverURL, "/"),
		token:       token,
		insecureTLS: insecureTLS,
		onLog:       onLog,
		recvCh:      make(chan RelayPacket, 256),
	}
}

func (r *RelayClient) logf(format string, args ...interface{}) {
	if r.onLog != nil {
		r.onLog(fmt.Sprintf("relay: "+format, args...))
	}
}

// Recv returns the channel relayed packets arrive on. Callers should
// keep draining it; a full channel silently drops new packets rather
// than blocking the relay's read loop (same loss semantics as UDP).
func (r *RelayClient) Recv() <-chan RelayPacket { return r.recvCh }

// Start begins the connect-retry loop in the background. Safe to call
// once; Stop() ends it for good.
func (r *RelayClient) Start() {
	go r.loop()
}

func (r *RelayClient) Stop() {
	r.mu.Lock()
	r.closed = true
	c := r.conn
	r.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

func (r *RelayClient) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func (r *RelayClient) loop() {
	for !r.isClosed() {
		if err := r.dialAndServe(); err != nil {
			r.logf("connection lost: %v", err)
		}
		if r.isClosed() {
			return
		}
		time.Sleep(5 * time.Second)
	}
}

func (r *RelayClient) dialAndServe() error {
	u, err := url.Parse(r.serverURL)
	if err != nil {
		return err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	var raw net.Conn
	if u.Scheme == "https" {
		raw, err = tls.Dial("tcp", host, &tls.Config{InsecureSkipVerify: r.insecureTLS})
	} else {
		raw, err = net.Dial("tcp", host)
	}
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer raw.Close()

	req, err := http.NewRequest("GET", r.serverURL+"/api/relay", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "govpn-relay")
	if err := req.Write(raw); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		resp.Body.Close()
		return fmt.Errorf("relay handshake failed: %s", resp.Status)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.conn = raw
	r.writer = bufio.NewWriter(raw)
	r.mu.Unlock()
	r.logf("connected")

	defer func() {
		r.mu.Lock()
		r.conn = nil
		r.writer = nil
		r.mu.Unlock()
	}()

	for {
		srcLen, err := br.ReadByte()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		srcBuf := make([]byte, srcLen)
		if _, err := io.ReadFull(br, srcBuf); err != nil {
			return fmt.Errorf("read src: %w", err)
		}
		var payLen uint16
		if err := binary.Read(br, binary.BigEndian, &payLen); err != nil {
			return fmt.Errorf("read len: %w", err)
		}
		if int(payLen) > maxRelayPayload {
			return fmt.Errorf("oversized frame: %d", payLen)
		}
		payload := make([]byte, payLen)
		if _, err := io.ReadFull(br, payload); err != nil {
			return fmt.Errorf("read payload: %w", err)
		}
		select {
		case r.recvCh <- RelayPacket{SrcPubKey: string(srcBuf), Payload: payload}:
		default:
			// receiver not keeping up; drop, same as a lost UDP packet.
		}
	}
}

// SendTo relays a raw packet to destPubKey through the server. A
// no-op (silent) error when not currently connected — callers treat
// this exactly like a best-effort UDP send that might just get lost.
func (r *RelayClient) SendTo(destPubKey string, payload []byte) error {
	if len(destPubKey) > 255 {
		return fmt.Errorf("pubkey too long for relay framing")
	}
	r.mu.Lock()
	w := r.writer
	r.mu.Unlock()
	if w == nil {
		return fmt.Errorf("relay not connected")
	}
	frame := make([]byte, 0, 1+len(destPubKey)+2+len(payload))
	frame = append(frame, byte(len(destPubKey)))
	frame = append(frame, destPubKey...)
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(payload)))
	frame = append(frame, lenBuf[:]...)
	frame = append(frame, payload...)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writer == nil {
		return fmt.Errorf("relay not connected")
	}
	if _, err := r.writer.Write(frame); err != nil {
		return err
	}
	return r.writer.Flush()
}
