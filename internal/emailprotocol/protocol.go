// Package emailprotocol defines enrollment wire messages shared by the client and server.
package emailprotocol

import (
	"errors"
	"net/mail"
	"strings"
)

// Request identifies the mailbox and exact device public key being enrolled.
type Request struct {
	Email     string `json:"email"`
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
}

// Receipt is deliberately identical for allowed, unknown, throttled and delivery-failed recipients.
type Receipt struct {
	ID        string `json:"id"`
	ExpiresAt int64  `json:"expiresAt"`
	Realm     string `json:"realm"`
}

// Proof binds mailbox access to possession of the proposed device key.
type Proof struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Signature string `json:"signature"`
}

// EnrollmentMessage domain-separates device possession proofs from authenticated API requests.
func EnrollmentMessage(realm, id string) []byte {
	return []byte("hostlier-enrollment-v1\x00" + realm + "\x00" + id)
}

// Address accepts a single normalized bare mailbox, excluding display-name or header injection syntax.
func Address(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil || parsed.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid email address")
	}
	return value, nil
}
