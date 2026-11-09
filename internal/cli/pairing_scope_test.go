package cli

import "testing"

// TestPairingCodeWithoutLimits sends the empty permission object to the protected owner endpoint.
func TestPairingCodeWithoutLimits(t *testing.T) {
	fake := &fakeClient{}
	// Inspect the real command's serialized request without issuing a live credential.
	fake.reply = func(c call) (any, error) {
		body := c.body.(map[string]any)
		scope := body["scope"].(map[string]any)
		// No finite defaults or empty application names may sneak into an unrestricted request.
		if c.path != "/v1/access/pair/code" || body["id"] != "pending" || len(scope) != 0 {
			t.Fatal("unexpected pairing approval", c.path, body)
		}
		return map[string]string{"access": "all"}, nil
	}
	_, err := invoke([]string{"pairing", "code", "pending", "--socket", "/tmp/controller.sock"}, "", Options{Client: fake, Home: t.TempDir()})
	// The command must accept an entirely omitted permission selection.
	if err != nil || len(fake.calls) != 1 {
		t.Fatal("empty pairing rejected", err)
	}
}

// TestPairingCodePartialLimitsReject prevents CLI input mistakes from becoming unlimited grants.
func TestPairingCodePartialLimitsReject(t *testing.T) {
	// Each partial or malformed selection must fail before contacting the controller.
	for _, flags := range [][]string{{"--app", "api"}, {"--secrets", "key"}, {"--for", "0s"}, {"--max-uses", "0"}, {"--app", ""}, {"--scope", "unknown"}, {"--scope", "deploy"}} {
		fake := &fakeClient{}
		args := append([]string{"pairing", "code", "pending", "--socket", "/tmp/controller.sock"}, flags...)
		_, err := invoke(args, "", Options{Client: fake, Home: t.TempDir()})
		// Invalid limits must not fall back to all access.
		if err == nil || len(fake.calls) != 0 {
			t.Fatal("invalid selection sent to controller", flags, err)
		}
	}
}
