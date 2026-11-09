package controller

// TunnelLease retains hostname ownership and a hashed connector credential across restarts.
type TunnelLease struct {
	Name          string `json:"name"`
	Owner         string `json:"owner"`
	BridgeID      string `json:"bridgeId"`
	URL           string `json:"url"`
	ConnectURL    string `json:"connectUrl"`
	Connections   int    `json:"connections"`
	ExpiresAt     int64  `json:"expiresAt"`
	Revoked       bool   `json:"revoked"`
	TokenVerifier string `json:"tokenVerifier"`
}

// tunnelRequest binds the connector credential and bridge label to a finite credential lifetime.
type tunnelRequest struct {
	Name            string `json:"name"`
	BridgeID        string `json:"bridgeId"`
	Connections     int    `json:"connections"`
	DurationSeconds int64  `json:"durationSeconds"`
	TokenVerifier   string `json:"tokenVerifier"`
}

// tunnelView excludes credential verifiers from public management responses.
type tunnelView struct {
	Name        string `json:"name"`
	BridgeID    string `json:"bridgeId"`
	URL         string `json:"url"`
	ConnectURL  string `json:"connectUrl"`
	Connections int    `json:"connections"`
	ExpiresAt   int64  `json:"expiresAt"`
	Status      string `json:"status"`
}
