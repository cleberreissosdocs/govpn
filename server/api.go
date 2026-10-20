package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"govpn/common"
)

type API struct {
	store      *StoreHolder
	subnetCIDR string
	relay      *relayRegistry
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func (a *API) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req common.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.AuthKey == "" || req.PubKey == "" {
		http.Error(w, "auth_key and pub_key required", http.StatusBadRequest)
		return
	}
	token, ip, err := a.store.Get().RegisterDevice(req.AuthKey, req.PubKey, req.Hostname)
	if err != nil {
		log.Printf("register failed for %s: %v", req.Hostname, err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	resp := common.RegisterResponse{
		DeviceToken: token,
		AssignedIP:  ip,
		SubnetCIDR:  a.subnetCIDR,
	}
	log.Printf("registered device hostname=%s pubkey=%s ip=%s", req.Hostname, req.PubKey, ip)
	json.NewEncoder(w).Encode(resp)
}

func (a *API) handlePeers(w http.ResponseWriter, r *http.Request) {
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
	rows, err := a.store.Get().ListPeers(pubKey)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp := common.PeerListResponse{}
	for _, d := range rows {
		resp.Peers = append(resp.Peers, common.Peer{
			PubKey:   d.PubKey,
			IP:       d.IP,
			Endpoint: d.Endpoint,
			Hostname: d.Hostname,
		})
	}
	json.NewEncoder(w).Encode(resp)
}

func (a *API) handleBeacon(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	_, _, err := a.store.Get().DeviceByToken(token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	var req common.BeaconRequest
	json.NewDecoder(r.Body).Decode(&req)

	// The client's source address as seen by the server is our cheap
	// "STUN" observation of its public endpoint. If it told us its
	// WireGuard listen port, substitute that port (the HTTP conn port
	// almost never matches, but with most home routers doing
	// endpoint-independent mapping this still gives a usable guess).
	host, _, _ := splitHostPort(r.RemoteAddr)
	endpoint := host
	if req.ListenPort > 0 && host != "" {
		endpoint = host + ":" + itoa(req.ListenPort)
	}
	if endpoint != "" {
		a.store.Get().UpdateEndpoint(token, endpoint)
	}
	w.WriteHeader(http.StatusNoContent)
}

func splitHostPort(addr string) (string, string, error) {
	return netSplitHostPort(addr)
}
