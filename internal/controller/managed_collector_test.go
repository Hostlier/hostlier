package controller

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hostlier/internal/collectorimage"
	"hostlier/internal/model"
)

// supervisedFixture records runtime attempts without launching external processes.
type supervisedFixture struct {
	recordingRuntime
	guard    sync.Mutex
	attempts []model.CollectorDeployment
	failure  bool
}

// TestCollectorRuntimeFailure only publishes diagnostics the runtime marked safe.
func TestCollectorRuntimeFailure(t *testing.T) {
	public := collectorRuntimeFailure(model.Err("COLLECTOR_NOT_READY", "Collector process is not ready"))
	if !strings.Contains(public.Error(), "Collector process is not ready") {
		t.Fatal(public)
	}
	private := collectorRuntimeFailure(errors.New("token=secret-value"))
	if strings.Contains(private.Error(), "secret-value") {
		t.Fatal(private)
	}
}

// EnsureCollector simulates idempotent supervision and a retryable runtime failure.
func (r *supervisedFixture) EnsureCollector(_ context.Context, value model.CollectorDeployment) error {
	r.guard.Lock()
	defer r.guard.Unlock()
	r.attempts = append(r.attempts, value)
	// Require the fixture result before continuing the authority or lifecycle check.
	if r.failure {
		return errors.New("sensitive runtime failure " + value.Token)
	}
	return nil
}

// managedFixture provides real persistent state and harmless synthetic OCI image assets.
func managedFixture(t *testing.T) (*Controller, *supervisedFixture, *time.Time) {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	runtime := &supervisedFixture{}
	// Require the fixture result before continuing the authority or lifecycle check.
	c := newTestController(t, root, runtime, func() time.Time { return now })
	c.opts.ManagedCollector = true
	binary := filepath.Join(t.TempDir(), "binary")
	// Require the fixture result before continuing the authority or lifecycle check.
	if err := os.WriteFile(binary, []byte("inert fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	// Require the fixture result before continuing the authority or lifecycle check.
	if err := collectorimage.Build(filepath.Join(root, "collector-image"), []collectorimage.PlatformInput{{Architecture: "arm64", BinaryPath: binary, CAPath: binary}}); err != nil {
		t.Fatal(err)
	}
	// Require the fixture result before continuing the authority or lifecycle check.
	t.Cleanup(func() { c.Close(context.Background()) })
	return c, runtime, &now
}

// TestManagedCollectorRotation retains working authority until healthy replacement and retries pending credentials.
func TestManagedCollectorRotation(t *testing.T) {
	c, runtime, now := managedFixture(t)
	// Require the fixture result before continuing the authority or lifecycle check.
	if err := c.reconcileManagedCollector(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := runtime.attempts[0].Token
	*now = now.Add(28 * 24 * time.Hour)
	runtime.failure = true
	err := c.reconcileManagedCollector(context.Background())
	c.recordManagedCollector(err)
	// Require the fixture result before continuing the authority or lifecycle check.
	if err == nil || len(c.state.TelemetryCollectors) != 2 {
		t.Fatal("replacement did not retain active verifier", err)
	}
	pending := runtime.attempts[1].Token
	verifyPendingRotation(t, c, runtime, first, pending)
	runtime.failure = false
	// Require the fixture result before continuing the authority or lifecycle check.
	if err = c.reconcileManagedCollector(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Require the fixture result before continuing the authority or lifecycle check.
	if managedTokenValid(c, first) || !managedTokenValid(c, pending) || len(c.state.TelemetryCollectors) != 1 {
		t.Fatal("old verifier not retired")
	}
	verifyManagedRestart(t, c, runtime, pending)
}

// verifyManagedRestart confirms private checkpoint reuse across controller process lifetimes.
func verifyManagedRestart(t *testing.T, c *Controller, runtime *supervisedFixture, token string) {
	t.Helper()
	options := c.opts
	// Require the fixture result before continuing the authority or lifecycle check.
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, err := New(options)
	// Require the fixture result before continuing the authority or lifecycle check.
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	// Require the fixture result before continuing the authority or lifecycle check.
	if err = next.reconcileManagedCollector(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Require the fixture result before continuing the authority or lifecycle check.
	if runtime.attempts[len(runtime.attempts)-1].Token != token {
		t.Fatal("restart rotated an unexpired working token")
	}
}

// managedTokenValid checks actual scoped bearer authority rather than internal verifier contents.
func managedTokenValid(c *Controller, token string) bool {
	request := httptest.NewRequest("POST", "/v1/telemetry/otlp/logs", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	_, err := c.telemetryAuthority(request)
	return err == nil
}

// TestManagedRegistryAuthority exercises the real controller wrapper around the read-only OCI handler.
func TestManagedRegistryAuthority(t *testing.T) {
	c, runtime, _ := managedFixture(t)
	// Require the fixture result before continuing the authority or lifecycle check.
	if err := c.reconcileManagedCollector(context.Background()); err != nil {
		t.Fatal(err)
	}
	token := runtime.attempts[0].Token
	// Require the fixture result before continuing the authority or lifecycle check.
	for _, test := range []struct {
		method, path, auth string
		status             int
	}{
		{"GET", "/v2/", "", 401},
		{"GET", "/v2/", "basic", 200},
		{"HEAD", "/v2/hostlier/collector/manifests/" + c.managed.image.Digest(), "bearer", 200},
		{"GET", "/v2/foreign/manifests/latest", "basic", 404},
		{"PUT", "/v2/hostlier/collector/blobs/uploads/", "basic", 405},
		{"GET", "/v1/status", "basic", 401},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		// Require the fixture result before continuing the authority or lifecycle check.
		if test.auth == "basic" {
			request.SetBasicAuth("collector", token)
		}
		// Require the fixture result before continuing the authority or lifecycle check.
		if test.auth == "bearer" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		c.Handler().ServeHTTP(response, request)
		// Require the fixture result before continuing the authority or lifecycle check.
		if response.Code != test.status {
			t.Fatalf("%s %s: %d", test.method, test.path, response.Code)
		}
		// Require the fixture result before continuing the authority or lifecycle check.
		if test.auth == "" && !strings.HasPrefix(response.Header().Get("WWW-Authenticate"), "Basic ") {
			t.Fatal("missing registry challenge")
		}
	}
}

// TestManagedCredentialRevocationRace prevents provisioning from resurrecting revoked authority.
func TestManagedCredentialRevocationRace(t *testing.T) {
	c, _, _ := managedFixture(t)
	credentials, err := c.prepareManagedCredential()
	// Require the fixture result before continuing the authority or lifecycle check.
	if err != nil {
		t.Fatal(err)
	}
	token := credentials.Pending
	c.mu.Lock()
	_, err = c.revokeTelemetryCollector(token.ID)
	c.mu.Unlock()
	// Require the fixture result before continuing the authority or lifecycle check.
	if err != nil {
		t.Fatal(err)
	}
	// Require the fixture result before continuing the authority or lifecycle check.
	if err = c.completeManagedCredential(credentials, token); err == nil || managedTokenValid(c, token.Token) {
		t.Fatal("revoked credential resurrected")
	}
}

// TestManagedCredentialPrivateFile rejects redirected credential storage before any authority change.
func TestManagedCredentialPrivateFile(t *testing.T) {
	c, _, _ := managedFixture(t)
	target := filepath.Join(t.TempDir(), "foreign")
	// Require the fixture result before continuing the authority or lifecycle check.
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	// Require the fixture result before continuing the authority or lifecycle check.
	if err := os.Symlink(target, filepath.Join(c.opts.StateDir, "managed-collector.json")); err != nil {
		t.Fatal(err)
	}
	// Require the fixture result before continuing the authority or lifecycle check.
	if _, err := c.prepareManagedCredential(); err == nil || len(c.state.TelemetryCollectors) != 0 {
		t.Fatal("redirected credentials accepted")
	}
	data, _ := os.ReadFile(target)
	// Require the fixture result before continuing the authority or lifecycle check.
	if string(data) != "unchanged" {
		t.Fatal("unrelated file changed")
	}
}

// verifyPendingRotation checks overlapping authority and retry identity before a replacement is ready.
func verifyPendingRotation(t *testing.T, c *Controller, runtime *supervisedFixture, first, pending string) {
	t.Helper()
	// Require the fixture result before continuing the authority or lifecycle check.
	if pending == first || !managedTokenValid(c, first) || !managedTokenValid(c, pending) {
		t.Fatal("rotation interrupted authority")
	}
	// Require the fixture result before continuing the authority or lifecycle check.
	if strings.Contains(c.managedCollectorHealth().Reason, pending) {
		t.Fatal("token leaked to status")
	}
	// Require the fixture result before continuing the authority or lifecycle check.
	if err := c.reconcileManagedCollector(context.Background()); err == nil || runtime.attempts[2].Token != pending {
		t.Fatal("failed attempt minted duplicate credential")
	}
}
