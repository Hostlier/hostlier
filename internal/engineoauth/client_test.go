package engineoauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOrigin rejects credential-bearing, remote plaintext and ambiguous Engine URLs.
func TestOrigin(t *testing.T) {
	// Exercise every rejected origin independently.
	for _, value := range []string{"http://engine.example", "https://user:secret@engine.example", "https://engine.example/path", "https://engine.example/?token=x", "https://engine.example/#x", "https://engine.example/?"} {
		// Check the protocol invariant before continuing the scenario.
		if _, err := Origin(value); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
}

// TestDiscoveryPinsEndpoints prevents malicious discovery from redirecting token credentials.
func TestDiscoveryPinsEndpoints(t *testing.T) {
	var origin string
	// This issuer fixture validates requests through the real HTTP transport.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Metadata{Issuer: origin, AuthorizationEndpoint: origin + "/authorize", TokenEndpoint: "https://attacker.example/token", RevocationEndpoint: origin + "/revoke"})
	}))
	defer server.Close()
	origin = server.URL
	// Check the protocol invariant before continuing the scenario.
	if _, err := Discover(context.Background(), origin); err == nil {
		t.Fatal("cross-origin token endpoint accepted")
	}
}

// TestRequestNeverFollowsRedirect protects credentials even on same-origin redirect chains.
func TestRequestNeverFollowsRedirect(t *testing.T) {
	// This issuer fixture validates requests through the real HTTP transport.
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer destination.Close()
	// This issuer fixture validates requests through the real HTTP transport.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	// Check the protocol invariant before continuing the scenario.
	if err := Request(context.Background(), "POST", server.URL, "private", "", nil, nil); err == nil {
		t.Fatal("redirect accepted")
	}
}
