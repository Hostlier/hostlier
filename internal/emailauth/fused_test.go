package emailauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fusedFixture provides real verified TLS while retaining all email payloads inside the test process.
func fusedFixture(t *testing.T, handler http.HandlerFunc) (*Fused, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	sender, err := NewFused("resend", server.URL, "11111111-1111-4111-8111-111111111111", "execution-token", "sender@example.com")
	// A valid isolated Engine origin must be accepted before any request is exercised.
	if err != nil {
		t.Fatal(err)
	}
	sender.HTTP.Transport = server.Client().Transport
	return sender, server
}

// TestFusedEmailExecutionContract sends only the fixed enrollment contract and requires Engine plus app acknowledgment.
func TestFusedEmailExecutionContract(t *testing.T) {
	calls := 0
	// Real TLS verifies both the capability boundary and the wire envelope.
	sender, _ := fusedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		// The capability must reach only the exact app-family execution endpoint.
		if r.Method != "POST" || r.URL.Path != "/v1/apps/11111111-1111-4111-8111-111111111111/executions" || r.Header.Get("Authorization") != "Bearer execution-token" {
			t.Error("incorrect execution target or credential")
		}
		var payload struct {
			Operation string         `json:"operation"`
			Input     map[string]any `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		// The hosted execution API requires its explicit execute operation.
		if payload.Operation != "execute" {
			t.Error("missing execution operation")
		}
		// Engine and provider idempotency use the same random logical-delivery identity.
		if len(r.Header.Get("Idempotency-Key")) != 32 || payload.Input["deliveryId"] != r.Header.Get("Idempotency-Key") {
			t.Error("missing stable delivery identity")
		}
		assertEnrollmentInput(t, payload.Input)

		w.Write([]byte(`{"status":"succeeded","output":{"sent":true,"provider":"resend","messageId":"message-1"}}`))
	})
	err := sender.Send(context.Background(), Message{Email: "owner@example.com", Code: "012345", DeviceName: "Laptop", ExpiresInSeconds: 300})
	// One positive terminal receipt means provider acceptance, without a second attempt.
	if err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

// TestFusedEmailFailuresRemainBounded rejects queued/failed app runs, ambiguous receipts, redirects and oversized bodies without retry.
func TestFusedEmailFailuresRemainBounded(t *testing.T) {
	cases := []string{"redirect", "http-error", "oversized", `{}`, `{"status":"queued","output":{"sent":true,"provider":"resend","messageId":"id"}}`, `{"status":"failed","output":{"sent":true,"provider":"resend","messageId":"id"}}`, `{"status":"succeeded","output":{"sent":false}}`, `{"status":"succeeded","output":{"sent":true,"provider":"plunk","messageId":"id"}}`, `{"status":"succeeded","output":{"sent":true,"provider":"resend"}}`}
	// Each outcome has its own TLS server and request count so retries cannot hide across cases.
	for _, mode := range cases {
		// Isolate every simulated terminal or transport failure.
		t.Run(mode, func(t *testing.T) { assertFusedFailure(t, mode) })
	}
}

// assertFusedFailure checks public error redaction and the absence of automatic send retries.
func assertFusedFailure(t *testing.T, mode string) {
	t.Helper()
	calls := 0
	// Real TLS verifies both the capability boundary and the wire envelope.
	sender, _ := fusedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Treat each explicitly simulated protocol failure independently.
		switch mode {
		// Redirects must not forward an execution capability to a new endpoint.
		case "redirect":
			http.Redirect(w, r, "https://untrusted.example", 302)
		// Provider diagnostics must remain private even after an HTTP failure.
		case "http-error":
			http.Error(w, "PRIVATE_PROVIDER_DETAIL", 500)
		// Bounded reads reject oversized execution envelopes before decoding them.
		case "oversized":
			w.Write([]byte(strings.Repeat("x", 65537)))
		// All other cases are valid HTTP responses with unsuccessful application receipts.
		default:
			w.Write([]byte(mode))
		}
	})
	err := sender.Send(context.Background(), Message{Email: "owner@example.com"})
	// Only the generic delivery error crosses the enrollment API boundary.
	if err == nil || err.Error() != "email delivery unavailable" || calls != 1 {
		t.Fatal(err, calls)
	}
}

// TestFusedConfigurationRejectsLegacyProviderKeys keeps old raw API keys from accidentally being sent to Engine.
func TestFusedConfigurationRejectsLegacyProviderKeys(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "auth.yaml")
	os.WriteFile(config, []byte("allowedEmails: [owner@example.com]\nprovider: resend\nfrom: sender@example.com\napiKeyEnv: OLD_PROVIDER_SECRET\n"), 0600)
	_, err := Load(config, "controller.example.com")
	// Legacy field names fail strict decoding and cannot be interpreted as execution tokens.
	if err == nil {
		t.Fatal("legacy provider credential configuration accepted")
	}
	os.WriteFile(filepath.Join(root, "email-token"), []byte("execution-token\n"), 0600)
	os.WriteFile(config, []byte("allowedEmails: [owner@example.com]\nprovider: plunk\nfrom: sender@example.com\nfused:\n  tokenFile: email-token\n"), 0600)
	service, err := Load(config, "controller.example.com")
	// Relative execution-token references resolve only within the protected controller configuration directory.
	if err != nil {
		t.Fatal(err)
	}
	sender, ok := service.Sender.(*Fused)
	// The configured provider must resolve to the scoped hosted sender.
	if !ok || sender.Provider != "plunk" || sender.Token != "execution-token" || !strings.HasPrefix(sender.URL, DefaultEngineURL+"/v1/apps/") {
		t.Fatal("wrong hosted email sender")
	} // The selected provider now routes only through the hosted app.
}

// TestFusedEmailRejectsUnsafeTargetsAndUntrustedTLS protects the controller-held capability and verification code.
func TestFusedEmailRejectsUnsafeTargetsAndUntrustedTLS(t *testing.T) {
	// A bare Engine origin cannot contain credentials, paths or alternate request-routing data.
	for _, target := range []string{"http://eng.usefused.com", "https://user@eng.usefused.com", "https://eng.usefused.com/other", "https://eng.usefused.com?secret=value", "https://eng.usefused.com#other"} {
		_, err := NewFused("resend", target, DefaultEmailAppFamilyID, "execution-token", "sender@example.com")
		// Unsafe origins must fail before token-bearing request construction.
		if err == nil {
			t.Fatal("unsafe Engine origin accepted", target)
		} // Validation precedes construction of an authenticated request.
	}
	// An untrusted certificate must fail before entering this handler.
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted TLS reached Engine handler") }))
	defer server.Close()
	sender, _ := NewFused("resend", server.URL, DefaultEmailAppFamilyID, "execution-token", "sender@example.com")
	// A private test CA is never trusted implicitly or by disabling certificate checks.
	if sender.Send(context.Background(), Message{Email: "owner@example.com"}) == nil {
		t.Fatal("untrusted Engine certificate accepted")
	}
}

// assertEnrollmentInput keeps provider-specific HTML and raw credential fields out of the controller request.
func assertEnrollmentInput(t *testing.T, input map[string]any) {
	t.Helper()
	// The controller selects only a provider and fixed bounded enrollment context.
	if input["email"] != "owner@example.com" || input["provider"] != "resend" || input["from"] != "sender@example.com" || input["html"] != nil {
		t.Error("unexpected enrollment input")
	}
}
