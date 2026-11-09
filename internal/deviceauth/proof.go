// Package deviceauth binds API requests to the enrolled device, server session and exact request bytes.
package deviceauth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Message is the domain-separated transcript signed by each enrolled device.
func Message(epoch, method, uri, timestamp, nonce string, body []byte) []byte {
	hash := sha256.Sum256(body)
	return []byte(strings.Join([]string{"hostlier-device-v1", epoch, method, uri, timestamp, nonce, hex.EncodeToString(hash[:])}, "\x00"))
}
