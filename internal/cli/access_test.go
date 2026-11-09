package cli

import (
	"crypto/ed25519"
	"encoding/base64"
	"hostlier/internal/client"
	"strings"
	"testing"
)

// TestBrowserConnectBindsAndPersistsDeviceKey exercises the default URL/code flow and private storage.
func TestBrowserConnectBindsAndPersistsDeviceKey(t *testing.T) {
	fake := pairingCLIClient(t)
	home := t.TempDir()
	// Keep simulated transport and user input confined to the test.
	output, err := invoke([]string{"connect", "https://controller.example.com", "--name", "laptop"}, "", Options{Client: fake, Home: home, Prompt: func(question string, hidden bool) (string, error) {
		// Fail the test when the expected security invariant is violated.
		if !hidden || !strings.Contains(question, "controller") {
			t.Fatal("code prompt", question)
		}
		return "1234-5678", nil
	}})
	// Fail the test when the expected security invariant is violated.
	if err != nil {
		t.Fatal(err)
	}
	// Fail the test when the expected security invariant is violated.
	if strings.Contains(output, "secret-token") || strings.Contains(output, "private-request") {
		t.Fatal("credential leaked")
	}
	settings, err := client.NewStore(home).Get("laptop")
	// Fail the test when the expected security invariant is violated.
	if err != nil || settings.Token == "secret-token" || settings.Token == "" {
		t.Fatal("device key not saved with credential", err)
	}
}

// pairingCLIClient validates the device proof before simulating controller issuance.
func pairingCLIClient(t *testing.T) *fakeClient {
	t.Helper()
	var public ed25519.PublicKey
	// Keep simulated transport and user input confined to the test.
	return &fakeClient{reply: func(c call) (any, error) {
		body := c.body.(map[string]string)
		// Exercise this protocol boundary independently.
		switch c.path {
		// Exercise this protocol boundary independently.
		case "/v1/access/pair/start":
			public, _ = base64.RawStdEncoding.DecodeString(body["publicKey"])
			return map[string]string{"id": "request", "url": "https://controller.example.com/connect?request=request", "requestSecret": "private-request", "message": "signed-challenge"}, nil
		// Exercise this protocol boundary independently.
		case "/v1/access/pair/redeem":
			signature, _ := base64.RawStdEncoding.DecodeString(body["signature"])
			// Fail the test when the expected security invariant is violated.
			if body["code"] != "1234-5678" || body["requestSecret"] != "private-request" || !ed25519.Verify(public, []byte("signed-challenge"), signature) {
				t.Fatal("invalid device proof")
			}
			return map[string]string{"clientId": "device", "token": "secret-token"}, nil
		}
		t.Fatalf("unexpected endpoint %s", c.path)
		return nil, nil
	}}
}
