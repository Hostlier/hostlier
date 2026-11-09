package controller

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// TestStatusStreamTLSReconnectAndRevocation verifies transport, replacement recovery and live authority together.
func TestStatusStreamTLSReconnectAndRevocation(t *testing.T) {
	r := &observationRuntime{gate: make(chan struct{}), started: make(chan struct{}, 32)}
	close(r.gate)
	c := observationController(t, r)
	c.mu.Lock()
	issued := c.credentials("status watcher")
	c.mu.Unlock()
	server := httptest.NewUnstartedServer(c.Handler())
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	remote := statusTLSClient(t, server, issued["token"].(string))
	// A new connection always receives the latest replacement, even after missing intermediate samples.
	first := receiveStatusSample(t, remote)
	second := receiveStatusSample(t, remote)
	// Both subscriptions observe the same cached capture instead of starting independent runtime probes.
	if first.ID != second.ID || r.calls.Load() != 1 {
		t.Fatalf("subscriptions duplicated capture: %s %s calls=%d", first.ID, second.ID, r.calls.Load())
	}
	checkStatusRevocation(t, c, remote, issued["clientId"].(string))
}

// statusTLSClient trusts only the fixture certificate and preserves the production HTTPS checks.
func statusTLSClient(t *testing.T, server *httptest.Server, token string) *client.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	// Trust configuration is explicit; the test never disables certificate verification.
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	remote, err := client.NewGRPC(client.Options{URL: server.URL, Token: token, CAFile: path})
	// An invalid test TLS client must fail before any watch attempts.
	if err != nil {
		t.Fatal(err)
	}
	return remote
}

// receiveStatusSample acknowledges one completed observation and then closes only the viewer.
func receiveStatusSample(t *testing.T, remote *client.Client) model.StatusSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stop := errors.New("sample received")
	var sample model.StatusSnapshot
	// Ignore cold metadata and acknowledge the first actual shared capture.
	err := remote.WatchStatus(ctx, func(event client.StatusEvent) error {
		// Initial metadata is deliberately unobserved until shared workers complete.
		if event.Snapshot != nil && !event.Snapshot.ObservedAt.IsZero() {
			sample = *event.Snapshot
			return stop
		}
		return nil
	})
	// Consumer cancellation must not reconnect or mutate durable work.
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	return sample
}

// checkStatusRevocation closes a currently subscribed client before exposing another captured state.
func checkStatusRevocation(t *testing.T, c *Controller, remote *client.Client, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	// One real client watches while its server-side credential is revoked.
	go func() {
		// Record stream admission without delaying delivery of cached status.
		done <- remote.WatchStatus(ctx, func(client.StatusEvent) error {
			// Signal at most one outstanding client notification.
			select {
			// Wake the revocation test after the initial observation.
			case started <- struct{}{}:
			// Additional notifications do not block the client callback.
			default:
			}
			return nil
		})
	}()
	awaitProbe(t, started)
	c.mu.Lock()
	c.state.Clients[id].Revoked = true
	c.mu.Unlock()
	// Publishing another immutable sample wakes authentication without waiting for heartbeat rotation.
	c.captureStatus()
	err := <-done
	var public *model.Error
	// Permanent authorization failure must end the reconnect loop.
	if !errors.As(err, &public) || public.Code != "UNAUTHORIZED" {
		t.Fatalf("revocation failed: %v", err)
	}
}

// TestStatusObservationAdmission verifies anonymous reads and excess streams do not reach sampling.
func TestStatusObservationAdmission(t *testing.T) {
	c := observationController(t, &recordingRuntime{})
	status, _ := call(t, c, false, "GET", "/v1/status/snapshot", nil, "")
	// Cached state is no less privileged than ordinary inventory.
	if status != 401 {
		t.Fatal("anonymous snapshot admitted")
	}
	c.observations.streams = 32
	status, _ = call(t, c, true, "GET", "/v1/status/events", nil, "")
	// Admission fails before streaming response headers and before another subscription is registered.
	if status != 429 {
		t.Fatal("status subscriber budget ignored")
	}
}
