package controller

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

// TestPairPollingRequiresDeviceAndOwner exercises pending, invalid proof, approval and replay.
func TestPairPollingRequiresDeviceAndOwner(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	_, start := humanHTTP(t, c, false, "POST", "/v1/access/pair/start", map[string]string{"name": "laptop", "publicKey": base64.RawStdEncoding.EncodeToString(public)}, "", "")
	proof := map[string]any{"id": start["id"], "requestSecret": start["requestSecret"], "signature": base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, []byte(start["message"].(string))))}
	status, pending := humanHTTP(t, c, false, "POST", "/v1/access/pair/poll", proof, "", "")
	// Possession alone cannot create a credential before the owner approves.
	if status != 200 || pending["status"] != "pending" || len(c.state.Clients) != 0 {
		t.Fatal(status, pending)
	}
	proof["signature"] = "invalid"
	status, _ = humanHTTP(t, c, false, "POST", "/v1/access/pair/poll", proof, "", "")
	// Even pending status is restricted to the initiating private key.
	if status == 200 {
		t.Fatal("invalid device proof accepted")
	}
	proof["signature"] = base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, []byte(start["message"].(string))))
	scope := deploymentScope{Apps: []string{"api"}, Secrets: []string{"new-key"}, CreateSecrets: []string{"new-key"}, DurationSeconds: 60, MaxUses: 2}
	status, approval := humanHTTP(t, c, true, "POST", "/v1/access/pair/code", map[string]any{"id": start["id"], "scope": scope}, "", "")
	// Explicit creation permission breaks the missing-secret bootstrap cycle.
	if status != 200 {
		t.Fatal(status, approval)
	}
	status, issued := humanHTTP(t, c, false, "POST", "/v1/access/pair/poll", proof, "", "")
	// Browser approval must issue a complete device-bound credential.
	if status != 200 || issued["token"] == nil || len(c.state.Clients) != 1 {
		t.Fatal(status, issued)
	}
	status, _ = humanHTTP(t, c, false, "POST", "/v1/access/pair/poll", proof, "", "")
	// Consumed approval cannot mint another identity.
	if status == 200 {
		t.Fatal("pairing replay accepted")
	}
}
