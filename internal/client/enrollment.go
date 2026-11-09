package client

// Enrollment remembers how a profile authenticates, never tokens, codes or private keys.
type Enrollment struct {
	Method string `json:"method,omitempty"`
	Email  string `json:"email,omitempty"`
}
