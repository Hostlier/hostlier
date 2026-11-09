package cli

import (
	"context"
	"fmt"
	"hostlier/internal/model"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// finishBrowserPairing supports automatic completion with a fallback for older controllers.
func (s *session) finishBrowserPairing(remote Requester, address string, polling bool, proof map[string]string) (enrollmentCredential, error) {
	var credential enrollmentCredential
	// Old controllers still require their single-use code in the initiating terminal.
	if !polling {
		code, err := s.ask("Code shown by the controller: ", true)
		// Cancellation cannot consume or persist enrollment material.
		if err != nil {
			return credential, err
		}
		proof["code"] = strings.TrimSpace(code)
		err = remote.Request(s.ctx, "POST", "/v1/access/pair/redeem", proof, &credential)
		return credential, err
	}
	s.openPairingBrowser(address)
	fmt.Fprintln(s.errOut, "Waiting for approval in your browser. Press Ctrl+C to cancel; your saved profile will remain unchanged.")
	return s.pollBrowserPairing(remote, proof)
}

// pollBrowserPairing proves device possession on every poll and bounds abandoned browser approval.
func (s *session) pollBrowserPairing(remote Requester, proof map[string]string) (enrollmentCredential, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Minute)
	defer cancel()
	// A pending response never constitutes authority to save a credential.
	for {
		var result enrollmentCredential
		// Transport errors and controller rejection leave the old profile usable.
		if err := remote.Request(ctx, "POST", "/v1/access/pair/poll", proof, &result); err != nil {
			return result, err
		}
		// Issued credentials are validated again before durable storage.
		if result.Status != "pending" {
			return result, nil
		}
		// The bounded polling context also controls the inter-request delay.
		waiting := *s
		waiting.ctx = ctx
		// User cancellation promptly stops the loop without changing profiles.
		if err := waiting.pause(); err != nil {
			return result, err
		}
	}
}

// openPairingBrowser launches only a validated HTTPS URL and always leaves the printed URL as fallback.
func (s *session) openPairingBrowser(address string) {
	parsed, err := url.Parse(address)
	// Never hand local files, credentials, or shell-like protocols to the OS opener.
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return
	}
	opener := s.options.OpenBrowser
	// An injected prompt belongs to an embedding or test and must not unexpectedly launch native UI.
	if opener == nil && s.options.Prompt == nil {
		opener = openSystemBrowser
	}
	// Browser launch is optional; approval remains possible using the printed URL.
	if opener != nil {
		// Report a recoverable launch failure without aborting pairing.
		if err = opener(address); err != nil {
			fmt.Fprintln(s.errOut, "Could not open a browser; open the review URL above.")
		}
	}
}

// openSystemBrowser invokes the platform URL handler without shell interpolation.
func openSystemBrowser(address string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Each platform uses an argument-vector opener rather than a command shell.
	switch runtime.GOOS {
	// macOS delegates to the user's default browser.
	case "darwin":
		return exec.CommandContext(ctx, "open", address).Run()
	// Linux delegates to the desktop URL handler when available.
	case "linux":
		return exec.CommandContext(ctx, "xdg-open", address).Run()
	}
	return model.Err("BROWSER", "Open the review URL in your browser")
}
