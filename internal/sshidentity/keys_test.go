package sshidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestPublicKeyRejectsOptionsAndMultipleKeys protects the authorized_keys trust boundary.
func TestPublicKeyRejectsOptionsAndMultipleKeys(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil {
		t.Fatal(err)
	}
	raw := string(ssh.MarshalAuthorizedKey(key))
	canonical, fingerprint, err := PublicKey(raw)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil || fingerprint != ssh.FingerprintSHA256(key) || canonical != strings.TrimSpace(raw) {
		t.Fatal(err)
	}
	// Exercise each independent SSH input or selection case.
	for _, bad := range []string{"command=\"evil\" " + raw, raw + raw, "restrict " + raw, strings.Repeat("x", 1025)} {
		// Assert the SSH fixture outcome before advancing to the next boundary.
		if _, _, err := PublicKey(bad); err == nil {
			t.Fatal("accepted unsafe public key")
		}
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if UserName("device-a") == UserName("device-b") || len(UserName("device-a")) > 32 {
		t.Fatal("bad account identity")
	}
}
