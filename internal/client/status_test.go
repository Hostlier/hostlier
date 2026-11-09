package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestStatusWatchReconnectsToCompleteSnapshots proves partial data cannot replace acknowledged state.
func TestStatusWatchReconnectsToCompleteSnapshots(t *testing.T) {
	calls := 0
	seen := 0
	stop := errors.New("consumer finished")
	// Serve a truncated frame followed by one complete replacement over authenticated requests.
	c := &Client{URL: "https://controller.test", Token: "credential", HTTP: &http.Client{Transport: eventTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		// Every reconnect must carry authority and must not rely on a lost intermediate delta.
		if r.Header.Get("Authorization") != "Bearer credential" || r.URL.Path != "/v1/status/events" {
			t.Fatal("status authority missing")
		}
		// Interrupted JSON is never delivered as a snapshot.
		if calls == 1 {
			return eventResponse(200, "event: snapshot\ndata: {\"id\":"), nil
		}
		return eventResponse(200, "event: snapshot\ndata: {\"id\":\"epoch:2\",\"applications\":[],\"operations\":[]}\n\n"), nil
	})}}
	// Acknowledge the first complete sample and terminate through the exact consumer error.
	err := c.WatchStatus(context.Background(), func(event StatusEvent) error {
		// Connection-state notifications never masquerade as deployment measurements.
		if event.Snapshot != nil {
			seen++
			return stop
		}
		return nil
	})
	// A consumer error is permanent and the truncated first frame was discarded.
	if !errors.Is(err, stop) || calls != 2 || seen != 1 {
		t.Fatalf("calls=%d samples=%d err=%v", calls, seen, err)
	}
}

// TestStatusWatchStopsOnRevokedCredentials ensures semantic failures do not loop forever.
func TestStatusWatchStopsOnRevokedCredentials(t *testing.T) {
	calls := 0
	// Simulate revocation with one permanent authenticated API rejection.
	c := &Client{URL: "https://controller.test", HTTP: &http.Client{Transport: eventTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return eventResponse(401, `{"error":{"code":"UNAUTHORIZED","message":"Authentication required"}}`), nil
	})}}
	// Discard events while checking that authorization failure is returned directly.
	err := c.WatchStatus(context.Background(), func(StatusEvent) error { return nil })
	// Revocation must be visible after one request rather than hidden by reconnection.
	if !eventErrorCode(err, "UNAUTHORIZED") || calls != 1 {
		t.Fatal("revoked status client retried", err)
	}
}

// TestStatusParserBoundsAndConsumerEOF preserves frame and acknowledgement guarantees.
func TestStatusParserBoundsAndConsumerEOF(t *testing.T) {
	valid := "event: snapshot\ndata: {\"id\":\"epoch:1\",\"applications\":[],\"operations\":[]}\n\n"
	// Simulate a local consumer whose writer has closed.
	err := readStatusFrames(strings.NewReader(valid), func(StatusEvent) error { return io.EOF })
	var delivery *eventConsumerFailure
	// Writer EOF is a consumer failure, never an instruction to reconnect.
	if !errors.As(err, &delivery) {
		t.Fatal("consumer EOF lost identity")
	}
	// A no-op consumer must never receive oversized JSON.
	err = readStatusFrames(strings.NewReader("data: "+strings.Repeat("x", MaxOperationFrameBytes)), func(StatusEvent) error { return nil })
	// A malicious oversized snapshot cannot grow memory without bound.
	if !eventErrorCode(err, "EVENT_LIMIT") {
		t.Fatal("oversized status frame accepted", err)
	}
}
