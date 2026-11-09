package client

import (
	"crypto/ed25519"
	"net/http"
	"time"
)

// Options selects client transport, trust roots, credentials and request deadline.
type Options struct {
	URL, SocketPath, Token, CAFile string
	Timeout                        time.Duration
}

// Client owns verified HTTP transport and its server-scoped bearer credential.
type Client struct {
	deviceKey          ed25519.PrivateKey
	HTTP               *http.Client
	URL, Token, Target string
}
