// Package sshidentity validates public SSH identities shared by the CLI and controller.
package sshidentity

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
	"hostlier/internal/model"
)

// PublicKey accepts a single Ed25519 key without caller-controlled authorized_keys options.
func PublicKey(raw string) (string, string, error) {
	// Reject ambiguous state or honor the explicit SSH selection.
	if len(raw) > 1024 {
		return "", "", model.Err("SSH_KEY", "Supply one Ed25519 SSH public key")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(raw))
	// Preserve failure before publishing or using SSH authority.
	if err != nil || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return "", "", model.Err("SSH_KEY", "Supply one Ed25519 SSH public key without options")
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if key.Type() != ssh.KeyAlgoED25519 {
		return "", "", model.Err("SSH_KEY", "SSH access requires an Ed25519 key")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), ssh.FingerprintSHA256(key), nil
}

// UserName binds a dedicated Linux identity to one controller device, never to caller input.
func UserName(actor string) string { return fmt.Sprintf("hlssh-%x", sha256.Sum256([]byte(actor)))[:22] }
