package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"hostlier/internal/model"
	"io"
	"strings"
	"testing"
	"time"
)

// TestFirstConnectionPromptsForSSHAndOpensEnrollment exercises the ordinary connect command.
func TestFirstConnectionPromptsForSSHAndOpensEnrollment(t *testing.T) {
	fake, _ := pollingCLIClient(t)
	original := fake.reply
	// A new controller advertises first-owner setup on the existing pairing response.
	fake.reply = func(c call) (any, error) {
		value, err := original(c)
		// Only the initial response carries owner discovery.
		if c.path == "/v1/access/pair/start" {
			value.(map[string]any)["ownerSetupRequired"] = true
		}
		return value, err
	}
	var opened string
	var arguments []string
	options := Options{Home: t.TempDir(), Client: fake, PollInterval: time.Millisecond,
		// The user chooses their SSH alias; no root account is guessed.
		Prompt: func(question string, hidden bool) (string, error) {
			// Setup asks for public SSH routing, never a secret in an application prompt.
			if hidden || !strings.Contains(question, "SSH destination") {
				t.Fatal(question)
			}
			return "test-vps", nil
		},
		// Capture the exact SSH invocation and return its short-lived controller result.
		RunSSH: func(_ context.Context, args []string) ([]byte, error) {
			arguments = args
			return json.Marshal(model.OwnerSetupResult{Status: "enroll", URL: "https://controller.example.com/security#enroll=secret&request=request", ExpiresAt: time.Now().Add(time.Minute).UnixMilli()})
		},
		// Browser navigation is recorded without invoking a real passkey or OS handler.
		OpenBrowser: func(address string) error { opened = address; return nil },
	}
	output, err := invoke([]string{"connect", "https://controller.example.com", "--name", "first"}, "", options)
	// The ordinary pairing poll completes only after the simulated owner approval.
	if err != nil || !strings.Contains(opened, "/security#enroll=") || !strings.Contains(output, `"connected":true`) {
		t.Fatal(output, err)
	}
	args := strings.Join(arguments, " ")
	// Bootstrap uses verified SSH and the controller executable, never a VPS hostlier CLI.
	if !strings.Contains(args, "StrictHostKeyChecking=ask") || !strings.Contains(args, "ForwardAgent=no") || !strings.Contains(args, "-- test-vps hostlier-controller bootstrap") {
		t.Fatal(args)
	}
}

// TestSSHBootstrapFailureLeavesSavedProfileIntact prevents partial setup from replacing credentials.
func TestSSHBootstrapFailureLeavesSavedProfileIntact(t *testing.T) {
	store := renewalStore(t)
	// Cancel the SSH transport after device preparation but before any owner link is returned.
	_, err := invoke([]string{"connect", "do-test", "--ssh", "root@vps"}, "", Options{Home: store.Home, Client: setupPollingClient(t), Prompt: func(string, bool) (string, error) { return "", nil }, RunSSH: func(context.Context, []string) ([]byte, error) { return nil, context.Canceled }})
	saved, _ := store.Get("do-test")
	// Cancellation cannot discard the prior identity or change the selected server.
	if !errors.Is(err, context.Canceled) || saved.Token != "old-do-test" {
		t.Fatal("cancelled SSH changed profile", err)
	}
}

// setupPollingClient supplies a controller supporting automatic pairing without initial owner hints.
func setupPollingClient(t *testing.T) *fakeClient { f, _ := pollingCLIClient(t); return f }

// TestSSHBootstrapRejectsWrongLinks checks cross-controller and cross-request browser redirection.
func TestSSHBootstrapRejectsWrongLinks(t *testing.T) {
	s := &session{serverTarget: "https://controller.example.com"}
	links := []string{"http://controller.example.com/security#enroll=s&request=r", "https://other.example.com/security#enroll=s&request=r", "https://user@controller.example.com/security#enroll=s&request=r", "https://controller.example.com/security?redirect=x#enroll=s&request=r", "https://controller.example.com/security#enroll=s&request=other", "https://controller.example.com/security#enroll=s&enroll=t&request=r"}
	// A link must match both the authenticated HTTPS origin and initiating device request.
	for _, link := range links {
		_, err := s.ownerSetupURL(model.OwnerSetupResult{Status: "enroll", URL: link, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, "r")
		// No malformed link may reach the browser opener.
		if err == nil {
			t.Fatal("unsafe link accepted", link)
		}
	}
}

// TestSSHProtocolOutputBoundUsesWriterPath guards against io.Copy bypassing the output limit.
func TestSSHProtocolOutputBoundUsesWriterPath(t *testing.T) {
	var output bootstrapOutput
	_, err := io.Copy(&output, bytes.NewReader(make([]byte, 65537)))
	// Embedded ReaderFrom methods must not bypass Write's bounded retention.
	if err == nil || output.data.Len() > 65536 {
		t.Fatal("SSH output exceeded limit")
	}
}

// TestSSHOptionsRejectedBeforeEnrollment prevents argument injection and accidental credential-flow changes.
func TestSSHOptionsRejectedBeforeEnrollment(t *testing.T) {
	cases := [][]string{{"--ssh", "-oProxyCommand=bad"}, {"--ssh", "root@host;bad"}, {"--ssh", ""}, {"--ssh", "host", "--ssh-socket", "relative"}, {"--ssh", "host", "--auth", "email", "--email", "owner@example.com"}, {"--ssh-sudo"}}
	// Invalid options must fail before creating a pending request or starting an SSH process.
	for _, flags := range cases {
		fake := &fakeClient{}
		// Inject a trusted terminal only; no real credentials or SSH processes participate.
		_, err := invoke(append([]string{"connect", "https://controller.example.com"}, flags...), "", Options{Home: t.TempDir(), Client: fake, Prompt: func(string, bool) (string, error) { return "", nil }})
		// None of these inputs may reach the controller enrollment endpoint.
		if err == nil || len(fake.calls) != 0 {
			t.Fatal("unsafe SSH options accepted", flags, err)
		}
	}
}
