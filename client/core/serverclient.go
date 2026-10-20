package core

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"govpn/common"
)

type ServerClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewServerClient builds a client for the coordination server's HTTP
// API. insecureTLS skips certificate verification — needed when the
// server is using its auto-generated self-signed cert (the default)
// instead of a real one; it still encrypts the connection, it just
// doesn't authenticate the server's identity against a CA, so prefer
// a real certificate (--tls-cert/--tls-key on the server) over relying
// on this for anything internet-facing long-term.
func NewServerClient(baseURL, token string, insecureTLS bool) *ServerClient {
	c := &http.Client{Timeout: 10 * time.Second}
	if insecureTLS {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return &ServerClient{baseURL: baseURL, token: token, http: c}
}

func (c *ServerClient) doJSON(method, path string, body interface{}, out interface{}) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *ServerClient) Register(authKey, pubKey, hostname string) (*common.RegisterResponse, error) {
	req := common.RegisterRequest{AuthKey: authKey, PubKey: pubKey, Hostname: hostname}
	var resp common.RegisterResponse
	if err := c.doJSON("POST", "/api/register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *ServerClient) Peers() (*common.PeerListResponse, error) {
	var resp common.PeerListResponse
	if err := c.doJSON("GET", "/api/peers", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *ServerClient) Beacon(listenPort int) error {
	req := common.BeaconRequest{ListenPort: listenPort}
	return c.doJSON("POST", "/api/beacon", req, nil)
}
