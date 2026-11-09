package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"hostlier/internal/client"
	"hostlier/internal/model"
	"strings"
	"testing"
	"time"
)

// renewalStore provides two saved profiles so renewal must preserve the current selection.
func renewalStore(t *testing.T) client.Store {
	t.Helper()
	store := client.NewStore(t.TempDir())
	// Establish independent credentials whose selection must not be changed by renewal.
	for _, name := range []string{"do-test", "other"} {
		// Setup failures cannot produce meaningful profile assertions.
		if err := store.Save(name, "https://controller.example.com", name, "old-"+name); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// TestReauthPreservesProfileOnCancellation verifies expired credentials need not be removed first.
func TestReauthPreservesProfileOnCancellation(t *testing.T) {
	store := renewalStore(t)
	before, _ := store.List()
	// Simulate the user cancelling the legacy approval prompt.
	_, err := invoke([]string{"reauth", "do-test"}, "", Options{Home: store.Home, Client: pairingCLIClient(t), Prompt: func(string, bool) (string, error) { return "", context.Canceled }})
	after, _ := store.List()
	old, _ := store.Get("do-test")
	// A failed approval must not alter credentials, identity, URL or selection.
	if !errors.Is(err, context.Canceled) || after.Current != before.Current || after.Servers["do-test"] != before.Servers["do-test"] || old.Token != "old-do-test" {
		t.Fatal("cancelled renewal changed profile", err)
	}
}

// TestReauthPollsAndReplacesOnlyNamedProfile exercises approval without any copied code.
func TestReauthPollsAndReplacesOnlyNamedProfile(t *testing.T) {
	store := renewalStore(t)
	fake, polls := pollingCLIClient(t)
	opened := ""
	output, err := invoke([]string{"reauth", "do-test"}, "", Options{Home: store.Home, Client: fake, PollInterval: time.Millisecond,
		// Record the opened review URL without launching native UI.
		OpenBrowser: func(address string) error { opened = address; return nil },
		// Automatic polling must never ask the user to copy a code.
		Prompt: func(string, bool) (string, error) { t.Fatal("unexpected code prompt"); return "", nil }})
	profiles, _ := store.List()
	renewed, _ := store.Get("do-test")
	other, _ := store.Get("other")
	// Renewal must preserve the current profile and never publish secret material.
	if err != nil || *polls != 2 || opened == "" || profiles.Current != "other" || renewed.Token == "old-do-test" || other.Token != "old-other" || !strings.Contains(output, `"reauthenticated":true`) {
		t.Fatal("renewal failed", err, output)
	}
	// Public command output contains neither the device token nor terminal pairing secret.
	if strings.Contains(output, "secret-token") || strings.Contains(output, "private-request") {
		t.Fatal("credential leaked")
	}
}

// pollingCLIClient verifies proof possession during both pending and approved polls.
func pollingCLIClient(t *testing.T) (*fakeClient, *int) {
	t.Helper()
	var public ed25519.PublicKey
	polls := 0
	// Verify both polling rounds against the original public key.
	fake := &fakeClient{reply: func(c call) (any, error) {
		body := c.body.(map[string]string)
		// A fake controller advertises polling only on the initial request.
		if c.path == "/v1/access/pair/start" {
			public, _ = base64.RawStdEncoding.DecodeString(body["publicKey"])
			return map[string]any{"id": "request", "requestSecret": "private-request", "message": "signed-challenge", "url": "https://controller.example.com/connect?request=request", "pollSupported": true}, nil
		}
		signature, _ := base64.RawStdEncoding.DecodeString(body["signature"])
		// Every poll must prove possession without inventing an approval code.
		if c.path != "/v1/access/pair/poll" || body["requestSecret"] != "private-request" || !ed25519.Verify(public, []byte("signed-challenge"), signature) {
			t.Fatal("invalid polling proof")
		}
		polls++
		// Pending responses may not be persisted as credentials.
		if polls == 1 {
			return map[string]string{"status": "pending"}, nil
		}
		return map[string]string{"clientId": "renewed-device", "token": "secret-token"}, nil
	}}
	return fake, &polls
}

// TestExpiredCredentialSuggestsReauth keeps the profile and supplies the exact recovery command.
func TestExpiredCredentialSuggestsReauth(t *testing.T) {
	store := renewalStore(t)
	// Simulate an authenticated API rejection without opening a network connection.
	fake := &fakeClient{reply: func(call) (any, error) {
		return nil, model.Err("UNAUTHORIZED", "Deployment credential expired or revoked")
	}}
	_, err := invoke([]string{"status", "api", "--server", "do-test"}, "", Options{Home: store.Home, Client: fake})
	// An expiry error must guide renewal rather than profile removal.
	if err == nil || !strings.Contains(err.Error(), "hostlier connect do-test") {
		t.Fatal(err)
	}
	saved, _ := store.Get("do-test")
	// Informational expiry handling cannot discard the user's saved URL or token.
	if saved.Token != "old-do-test" {
		t.Fatal("expired profile removed")
	}
}
