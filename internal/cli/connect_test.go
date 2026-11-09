package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"hostlier/internal/client"
	emailauth "hostlier/internal/emailprotocol"
	"strings"
	"testing"
	"time"
)

// TestConnectionSpellingsShareRenewal verifies one pipeline serves every supported spelling.
func TestConnectionSpellingsShareRenewal(t *testing.T) {
	cases := [][]string{{"connect", "do-test"}, {"connect", "https://controller.example.com/", "--name", "do-test"}, {"connect", "--server", "do-test"}, {"server", "connect", "do-test"}, {"reauth", "do-test"}, {"server", "reauth", "do-test"}}
	// Each spelling must renew without selecting or deleting another profile.
	for _, args := range cases {
		// Isolate stored credentials and generated keys between equivalent invocations.
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			store := renewalStore(t)
			// Legacy controller fallback exercises the same stored credential path.
			output, err := invoke(args, "", Options{Home: store.Home, Client: pairingCLIClient(t), Prompt: func(string, bool) (string, error) { return "1234-5678", nil }})
			profiles, _ := store.List()
			saved, _ := store.Get("do-test")
			// The selected profile and unrelated credentials must survive named renewal.
			if err != nil || profiles.Current != "other" || saved.Token == "old-do-test" || profiles.Servers["do-test"].Enrollment.Method != "browser" || !strings.Contains(output, `"name":"do-test"`) {
				t.Fatal(output, err)
			}
		})
	}
}

// TestConnectWithoutTargetRenewsSelectedProfile keeps the common renewal command short.
func TestConnectWithoutTargetRenewsSelectedProfile(t *testing.T) {
	store := renewalStore(t)
	// A selected profile resolves without requiring its URL or an old usable token.
	output, err := invoke([]string{"connect"}, "", Options{Home: store.Home, Client: pairingCLIClient(t), Prompt: func(string, bool) (string, error) { return "1234-5678", nil }})
	profiles, _ := store.List()
	// Renewal must use the current profile rather than inventing a default origin.
	if err != nil || !strings.Contains(output, `"name":"other"`) || profiles.Current != "other" {
		t.Fatal(output, err)
	}
}

// TestConnectionRejectsAmbiguityBeforeNetwork protects profile origins and method authority.
func TestConnectionRejectsAmbiguityBeforeNetwork(t *testing.T) {
	cases := [][]string{
		{"connect", "https://other.example.com", "--name", "do-test"},
		{"connect", "http://controller.example.com", "--name", "fresh"},
		{"connect", "do-test", "--server", "other"},
		{"connect", "do-test", "--name", "other"},
		{"connect", "do-test", "extra"},
		{"connect", "missing"},
		{"connect", "do-test", "--socket", "/tmp/controller.sock"},
		{"connect", "do-test", "--email", "owner@example.com", "--request"},
		{"connect", "do-test", "--request", "--token-stdin"},
		{"connect", "do-test", "--auth", "browser", "--token-stdin"},
		{"connect", "do-test", "--auth", "unknown"},
	}
	// Invalid requests must leave profile data and remote enrollment entirely untouched.
	for _, args := range cases {
		// Isolate each rejection to verify it happens before invoking the transport.
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			store := renewalStore(t)
			fake := &fakeClient{}
			_, err := invoke(args, "must-not-consume", Options{Home: store.Home, Client: fake})
			saved, _ := store.Get("do-test")
			// Validation failures cannot replace credentials or contact a controller.
			if err == nil || len(fake.calls) != 0 || saved.Token != "old-do-test" {
				t.Fatal("unsafe connection", err)
			}
		})
	}
}

// TestConnectCancellationPreservesAuthenticationPreferences checks method changes commit only on success.
func TestConnectCancellationPreservesAuthenticationPreferences(t *testing.T) {
	store := client.NewStore(t.TempDir())
	// Start with a saved email identity before requesting a browser-method change.
	if err := store.SaveEnrollment("test", "https://controller.example.com", "old", "old-token", client.Enrollment{Method: "email", Email: "owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.List()
	// Cancelling a new method must not alter the old method or credential.
	_, err := invoke([]string{"connect", "test", "--auth", "browser"}, "", Options{Home: store.Home, Client: pairingCLIClient(t), Prompt: func(string, bool) (string, error) { return "", context.Canceled }})
	after, _ := store.List()
	saved, _ := store.Get("test")
	// The profile update is atomic with successful credential issuance.
	if !errors.Is(err, context.Canceled) || before.Servers["test"] != after.Servers["test"] || saved.Token != "old-token" {
		t.Fatal("cancelled method switch changed profile", err)
	}
}

// TestEmailConnectionRemembersMethod exercises saved email renewal without repeating flags.
func TestEmailConnectionRemembersMethod(t *testing.T) {
	home := t.TempDir()
	// Fake controller verifies mailbox identity and actual generated device signatures.
	fake := emailConnectionClient(t)
	// A trusted-terminal fixture provides only the fresh emailed code.
	options := Options{Home: home, Client: fake, Prompt: func(string, bool) (string, error) { return "123456", nil }}
	_, err := invoke([]string{"connect", "https://controller.example.com", "--name", "mail", "--auth", "email", "--email", "owner@example.com"}, "", options)
	// New connections must publish the authentication method with their device credential.
	if err != nil {
		t.Fatal(err)
	}
	_, err = invoke([]string{"connect", "mail"}, "", options)
	profiles, _ := client.NewStore(home).List()
	// Renewal must reuse email enrollment rather than silently switching to browser approval.
	if err != nil || profiles.Servers["mail"].Enrollment != (client.Enrollment{Method: "email", Email: "owner@example.com"}) || len(fake.calls) != 4 {
		t.Fatal("email renewal failed", err)
	}
}

// emailConnectionClient checks proof of possession on both initial and renewed email identities.
func emailConnectionClient(t *testing.T) *fakeClient {
	t.Helper()
	var public ed25519.PublicKey
	issuance := 0
	// Every request uses the enrollment endpoints, never an existing expired credential.
	return &fakeClient{reply: func(c call) (any, error) {
		// Email begin must use the remembered normalized address.
		if c.path == "/v1/email/begin" {
			b := c.body.(emailauth.Request)
			// Unexpected addresses would silently change the authenticated identity.
			if b.Email != "owner@example.com" {
				t.Fatal("email changed")
			}
			public, _ = base64.RawStdEncoding.DecodeString(b.PublicKey)
			return map[string]string{"id": "request", "realm": "realm"}, nil
		}
		// An implicit browser or administrator fallback would change authority.
		if c.path != "/v1/email/verify" {
			t.Fatal("unexpected enrollment endpoint", c.path)
		}
		b := c.body.(emailauth.Proof)
		sig, _ := base64.RawStdEncoding.DecodeString(b.Signature)
		// Verify fresh device proof rather than accepting arbitrary test credentials.
		if b.Code != "123456" || !ed25519.Verify(public, emailauth.EnrollmentMessage("realm", "request"), sig) {
			t.Fatal("invalid email proof")
		}
		issuance++
		return map[string]any{"clientId": strings.Repeat("d", issuance), "token": "device-token"}, nil
	}}
}

// TestTokenRenewalRequiresExplicitFreshInput prevents ordinary connect from consuming a pipeline.
func TestTokenRenewalRequiresExplicitFreshInput(t *testing.T) {
	store := client.NewStore(t.TempDir())
	// Persist only the method; one-time enrollment material is never retained.
	if err := store.SaveEnrollment("test", "https://controller.example.com", "old", "old-token", client.Enrollment{Method: "token"}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeClient{}
	_, err := invoke([]string{"connect", "test"}, "unrelated-input", Options{Home: store.Home, Client: fake})
	// The caller must explicitly opt into stdin consumption for each new token.
	if err == nil || !strings.Contains(err.Error(), "--token-stdin") || len(fake.calls) != 0 {
		t.Fatal("implicit token consumption", err)
	}
}

// TestAdministratorRequestUsesSharedMethodSelection verifies compatibility without a second workflow.
func TestAdministratorRequestUsesSharedMethodSelection(t *testing.T) {
	home := t.TempDir()
	issuance := 0
	// The administrator still explicitly approves each request on the controller.
	fake := &fakeClient{reply: func(c call) (any, error) {
		// Each initial or renewed connection creates a fresh bounded pending request.
		if c.path == "/v1/pairing/requests" {
			return map[string]any{"requestId": "request", "requestSecret": "private", "code": "owner-review", "expiresAt": time.Now().Add(time.Minute).UnixMilli()}, nil
		}
		// Do not substitute browser or token enrollment for this saved method.
		if c.path != "/v1/pairing/poll" {
			t.Fatal("unexpected endpoint", c.path)
		}
		issuance++
		return map[string]string{"clientId": strings.Repeat("r", issuance), "token": "credential"}, nil
	}}
	options := Options{Home: home, Client: fake, PollInterval: time.Millisecond}
	_, err := invoke([]string{"connect", "https://controller.example.com", "--name", "admin", "--auth", "request"}, "", options)
	// Explicit administrator enrollment must succeed before remembering the method.
	if err != nil {
		t.Fatal(err)
	}
	_, err = invoke([]string{"reauth", "admin"}, "", options)
	profiles, _ := client.NewStore(home).List()
	// The alias must reuse the same saved method without repeating its selection flags.
	if err != nil || profiles.Servers["admin"].Enrollment.Method != "request" || issuance != 2 {
		t.Fatal("request method changed", err)
	}
}

func TestUnnamedConnectionsCoexist(t *testing.T) {
	store := client.NewStore(t.TempDir())
	for _, host := range []string{"a", "b", "a"} {
		out, err := invoke([]string{"connect", "https://" + host + ".example.com", "--token-stdin"}, "token", Options{Home: store.Home, Client: &fakeClient{reply: func(c call) (any, error) {
			return map[string]string{"clientId": client.RequestID(), "token": "token"}, nil
		}}})
		if err != nil {
			t.Fatal(out, err)
		}
	}
	profiles, err := store.List()
	if err != nil || profiles.Servers["default"].URL != "https://a.example.com" || profiles.Servers["b-example-com"].URL != "https://b.example.com" || profiles.Current != "b-example-com" {
		t.Fatal(profiles, err)
	}
}
