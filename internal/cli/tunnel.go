package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"hostlier/internal/model"
)

// tunnelRegistration describes public lease metadata without client-specific forwarding settings.
type tunnelRegistration struct {
	Name        string `json:"name"`
	BridgeID    string `json:"bridgeId"`
	URL         string `json:"url"`
	ConnectURL  string `json:"connectUrl"`
	Connections int    `json:"connections"`
	ExpiresAt   int64  `json:"expiresAt"`
	Status      string `json:"status,omitempty"`
}

// tunnelCommand manages domain reservations through the existing approved controller transport.
func (s *session) tunnelCommand() error {
	// Select only the documented tunnel lifecycle commands.
	switch s.sub {
	// List reservations using the authenticated management connection.
	case "", "list":
		return s.getPrint("/v1/tunnels")
	// Read one reservation without changing its lifetime.
	case "status":
		// Require an explicit reservation name before contacting the controller.
		if s.target == "" {
			return model.Err("ARGUMENTS", "Tunnel name required")
		}
		return s.getPrint("/v1/tunnels/" + url.PathEscape(s.target))
	// Submit explicit revocation through the normal approval flow.
	case "revoke":
		// Require an explicit reservation name before contacting the controller.
		if s.target == "" {
			return model.Err("ARGUMENTS", "Tunnel name required")
		}
		var result any
		// Propagate the failed prerequisite before reporting success or serving traffic.
		if err := s.request("DELETE", "/v1/tunnels/"+url.PathEscape(s.target), nil, &result); err != nil {
			return err
		}
		return s.print(result)
	// Request a generic public tunnel reservation.
	case "create":
		return s.createTunnelRegistration()
	// Reject unsupported lifecycle commands before remote mutation.
	default:
		return model.Err("ARGUMENTS", "Usage: tunnel create NAME --bridge-id ID --from-env TOKEN_ENV [--for 24h]; tunnel list|status NAME|revoke NAME")
	}
}

// tunnelInput validates connector-neutral publication inputs before requesting approval.
func (s *session) tunnelInput() (map[string]any, error) {
	token := os.Getenv(s.flags["from-env"])
	// Reject missing, short or header-unsafe connector credentials.
	if len(token) < 32 || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		return nil, model.Err("ARGUMENTS", "--from-env must name a random tunnel token of 32–512 characters")
	}
	// Require an explicit reservation name before contacting the controller.
	if s.target == "" || s.flags["bridge-id"] == "" {
		return nil, model.Err("ARGUMENTS", "Tunnel name and --bridge-id required")
	}
	// Local service configuration belongs to the connector, never the hosting API.
	if s.flags["target"] != "" || s.flags["output"] != "" {
		return nil, model.Err("ARGUMENTS", "Use fused-bridge tunnel setup for local targets and connector configuration")
	}
	duration, err := tunnelDuration(s.flags["for"])
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err != nil {
		return nil, err
	}

	connections, err := tunnelConnectionCount(s.flags["connections"])
	// Propagate the failed prerequisite before reporting success or serving traffic.
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(token))
	return map[string]any{"name": s.target, "bridgeId": s.flags["bridge-id"], "connections": connections, "durationSeconds": int64(duration / time.Second), "tokenVerifier": hex.EncodeToString(hash[:])}, nil
}

// tunnelConnectionCount bounds the worker pool before contacting a controller.
func tunnelConnectionCount(value string) (int, error) {
	// Use the documented bounded default when the operator omits this option.
	if value == "" {
		return 4, nil
	}
	n, err := strconv.Atoi(value)
	// Keep the worker pool within the transport capacity limit.
	if err != nil || n < 1 || n > 16 {
		return 0, model.Err("ARGUMENTS", "Connections must be 1–16")
	}
	return n, nil
}

// createTunnelRegistration prints generic lease metadata; the caller retains its chosen connector token.
func (s *session) createTunnelRegistration() error {
	body, err := s.tunnelInput()
	// Invalid local input must not create a publication request.
	if err != nil {
		return err
	}
	var result tunnelRegistration
	// Existing owner approval governs publication for every remote client.
	if err = s.request("POST", "/v1/tunnels", body, &result); err != nil {
		return err
	}
	return s.print(result)
}

// tunnelDuration bounds approval lifetimes before any remote publication.
func tunnelDuration(value string) (time.Duration, error) {
	// Use the documented bounded default when the operator omits this option.
	if value == "" {
		return 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	// Keep the requested lifetime within the controller approval limits.
	if err != nil || duration < time.Minute || duration > 30*24*time.Hour {
		return 0, model.Err("ARGUMENTS", "Tunnel lifetime must be 1 minute–720 hours")
	}
	return duration, nil
}
