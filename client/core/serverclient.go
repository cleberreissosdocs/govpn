package core

import (
	"bytes"
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

func NewServerClient(baseURL, token string) *ServerClient {
	return &ServerClient{baseURL: baseURL, token: token, http: &http.Client{Timeout: 10 * time.Second}}
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
