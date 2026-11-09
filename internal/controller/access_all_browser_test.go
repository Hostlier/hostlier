package controller

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// TestBrowserEmptyScopeRequiresOwner proves empty permissions still require a verified passkey ceremony.
func TestBrowserEmptyScopeRequiresOwner(t *testing.T) {
	f := registerBrowserFixture(t)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	_, start := humanHTTP(t, f.c, false, "POST", "/v1/access/pair/start", map[string]string{"name": "Laptop", "publicKey": base64.RawStdEncoding.EncodeToString(public)}, "", "")
	proof := map[string]any{"id": start["id"], "requestSecret": start["requestSecret"], "signature": base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, []byte(start["message"].(string))))}
	status, pending := humanHTTP(t, f.c, false, "POST", "/v1/access/pair/poll", proof, "", "")
	// Empty permissions are a requested capability, never implicit authentication.
	if status != 200 || pending["status"] != "pending" {
		t.Fatal("unapproved credential issued")
	}
	status, begin := browserJSON(t, f.c, "/v1/access/passkey/pair/begin", map[string]any{"id": start["id"]}, "")
	// Omitting the entire scope is valid at the browser protocol boundary.
	if status != 200 {
		t.Fatal(status, begin)
	}
	ceremony := begin["ceremony"].(string)
	status, approved := browserJSON(t, f.c, "/v1/access/passkey/pair/finish", f.assertion(t, ceremony, 0x05, "https://"+f.c.state.Hostname), ceremony)
	// A real owner assertion must explicitly approve unrestricted access.
	if status != 200 || approved["access"] != "all" {
		t.Fatal(status, approved)
	}
	status, issued := humanHTTP(t, f.c, false, "POST", "/v1/access/pair/poll", proof, "", "")
	// Polling issues only after the authenticated browser decision.
	if status != 200 || issued["access"] != "all" || issued["expiresAt"] != float64(0) {
		t.Fatal("all-access issuance failed", status)
	}
	g := f.c.state.Authorizations[issued["authorizationId"].(string)]
	key := f.c.state.Human.Passkeys[g.KeyFingerprint]
	key.Revoked = true
	f.c.state.Human.Passkeys[g.KeyFingerprint] = key
	// Revoking the actual passkey invalidates all grants issued through it.
	if f.c.authorizationLive(g) {
		t.Fatal("revoked passkey retained authority")
	}
}
