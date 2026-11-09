// Package emailauth delivers enrollment codes without granting deployment authority.
package emailauth

import (
	"context"
	"hostlier/internal/emailprotocol"
)

// Message is the fixed, bounded contract shared by SMTP and the Fused Unified App.
type Message struct {
	Email             string `json:"email"`
	Code              string `json:"code"`
	ExpiresInSeconds  int    `json:"expiresInSeconds"`
	Controller        string `json:"controller"`
	DeviceName        string `json:"deviceName"`
	DeviceFingerprint string `json:"deviceFingerprint"`
}

// Sender acknowledges provider acceptance, never inbox delivery.
type Sender interface {
	Send(context.Context, Message) error
}

// Address validates the shared wire-format mailbox.
func Address(value string) (string, error) { return emailprotocol.Address(value) }
