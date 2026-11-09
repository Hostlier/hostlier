package cli

import "testing"

// TestAuthorizeSandboxRequest sends prefix, capacity, route domain and keep options without exact apps.
func TestAuthorizeSandboxRequest(t *testing.T) {
	fake := &fakeClient{}
	// Inspect the serialized window request.
	fake.reply = func(c call) (any, error) {
		body := c.body.(map[string]any)
		// The sandbox selector replaces the exact app list.
		if c.path != "/v1/authorizations" || body["appPrefix"] != "try-" || body["maxApps"] != 3 || body["routeDomain"] != "try.example.com" || body["keepApps"] != true || body["apps"] != nil {
			t.Fatal("unexpected window", c.path, body)
		}
		return map[string]string{"id": "grant"}, nil
	}
	args := []string{"authorize", "--device", "d", "--on", "local", "--app-prefix", "try-", "--max-apps", "3", "--route-domain", "try.example.com", "--keep-apps", "--scope", "deploy", "--for", "2h", "--socket", "/tmp/controller.sock"}
	_, err := invoke(args, "", Options{Client: fake, Home: t.TempDir()})
	// The request must reach the controller exactly once.
	if err != nil || len(fake.calls) != 1 {
		t.Fatal(err)
	}
}

// TestAuthorizeRejectsAmbiguousSelectors refuses mixed or missing app selectors before any request.
func TestAuthorizeRejectsAmbiguousSelectors(t *testing.T) {
	base := []string{"authorize", "--device", "d", "--on", "local", "--scope", "deploy", "--for", "2h", "--socket", "/tmp/controller.sock"}
	// Each selection is invalid on its own.
	for _, flags := range [][]string{{}, {"--app", "api", "--app-prefix", "try-"}, {"--app", "api", "--max-apps", "2"}, {"--app", "api", "--keep-apps"}, {"--app-prefix", "try-", "--max-apps", "x"}} {
		fake := &fakeClient{}
		_, err := invoke(append(append([]string{}, base...), flags...), "", Options{Client: fake, Home: t.TempDir()})
		// Invalid input must never contact the controller.
		if err == nil || len(fake.calls) != 0 {
			t.Fatal("accepted", flags, err)
		}
	}
}
