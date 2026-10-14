package common

// RegisterRequest is sent by a client on first run to join the mesh.
type RegisterRequest struct {
	AuthKey  string `json:"auth_key"`
	PubKey   string `json:"pub_key"` // WireGuard public key, base64
	Hostname string `json:"hostname"`
}

type RegisterResponse struct {
	DeviceToken string `json:"device_token"` // bearer token for subsequent calls
	AssignedIP  string `json:"assigned_ip"`  // e.g. 100.64.0.2
	SubnetCIDR  string `json:"subnet_cidr"`  // e.g. 100.64.0.0/10
}

type Peer struct {
	PubKey   string `json:"pub_key"`
	IP       string `json:"ip"`       // assigned mesh IP, no mask
	Endpoint string `json:"endpoint"` // ip:port as last observed by the server, may be empty
	Hostname string `json:"hostname"`
}

type PeerListResponse struct {
	Peers []Peer `json:"peers"`
}

// BeaconRequest is sent periodically by clients so the server can learn
// their observed public ip:port (simple STUN-like NAT discovery) and mark
// them as alive.
type BeaconRequest struct {
	ListenPort int `json:"listen_port"`
}
