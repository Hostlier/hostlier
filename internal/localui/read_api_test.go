package localui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/client"
)

// pageRemote lets tests hold diagnostic work independently of approval and status processing.
type pageRemote struct {
	calls   atomic.Int32
	started chan string
	release chan struct{}
	value   string
}

// WatchStatus leaves the synthetic stream unused in direct handler tests.
func (r *pageRemote) WatchStatus(ctx context.Context, _ func(client.StatusEvent) error) error {
	<-ctx.Done()
	return ctx.Err()
}

// Request returns a recognizable controller identity after optional concurrent-read synchronization.
func (r *pageRemote) Request(ctx context.Context, method, path string, body, result any) error {
	r.calls.Add(1)
	// Tests can hold a read worker while inspecting local cache and capacity behavior.
	if r.started != nil {
		r.started <- path
		// Held requests must honor the worker deadline and CLI shutdown.
		select {
		// Releasing a request permits its controller identity response.
		case <-r.release:
		// Cancellation interrupts held upstream work.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	data, _ := json.Marshal(map[string]string{"controller": r.value, "path": path})
	return json.Unmarshal(data, result)
}

// TestPageRoutesAndReadAuthority covers deep links, session isolation and fixed upstream path construction.
func TestPageRoutesAndReadAuthority(t *testing.T) {
	s, _ := testUIServer(t)
	// Every supported route returns the embedded shell, including a direct-link page refresh.
	for _, path := range []string{"/logs", "/tunnels/demo", "/overview", "/deployments", "/operations", "/observability", "/deployments/my-app", "/operations/op-1", "/resources", "/resources/cache", "/hosts", "/hosts/worker", "/recovery", "/recovery/job-1", "/access", "/settings"} {
		response := uiCall(s, "GET", path, "", "", "")
		// Direct page navigation must return the embedded shell.
		if response.Code != 200 || !strings.Contains(response.Body.String(), `id="page-content"`) {
			t.Fatalf("page %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	// Read endpoints preserve session requirements and reject encoded route escapes.
	for _, path := range []string{"/api/activity", "/api/tunnels", "/api/tunnels/demo", "/api/deployments", "/api/operations/op-1", "/api/observability?signal=metrics"} {
		// Missing local capabilities must never initiate upstream work.
		if response := uiCall(s, "GET", path, "", "", ""); response.Code != 403 {
			t.Fatalf("unprivileged read %s: %d", path, response.Code)
		}
	}
}

// TestPageReadPathValidation verifies the authenticated route allowlist and bounded history selectors.
func TestPageReadPathValidation(t *testing.T) {
	s, remote := testUIServer(t)
	// Valid detail reads map exactly to the supported controller contract.
	for local, upstream := range map[string]string{
		"/api/activity?app=my-app&q=ready&from=2026-11-01T10:00:00Z&to=2026-11-01T12:00:00Z&level=info": "/v1/activity?app=my-app&from=2026-11-01T10%3A00%3A00Z&level=info&limit=100&q=ready&to=2026-11-01T12%3A00%3A00Z",
		"/api/observability?signal=logs&before=12&beforeTime=2026-11-01T11:00:00Z&q=timeout":            "/v1/telemetry/history?before=12&beforeTime=2026-11-01T11%3A00%3A00Z&limit=100&q=timeout&signal=logs",
		"/api/tunnels": "/v1/tunnels", "/api/tunnels/demo": "/v1/tunnels/demo",
		"/api/resources": "/v1/resources?observed=false", "/api/resources/cache": "/v1/resources/cache", "/api/resources/cache/logs": "/v1/resources/cache/logs",
		"/api/hosts": "/v1/hosts", "/api/hosts/worker": "/v1/hosts/worker/proxy/host-state",
		"/api/recovery/jobs": "/v1/recovery-jobs", "/api/recovery/policies": "/v1/backup-policies", "/api/recovery/backups": "/v1/backups", "/api/recovery/destinations": "/v1/backup-destinations", "/api/recovery/job-1": "/v1/recovery-jobs/job-1",
		"/api/access/clients": "/v1/clients", "/api/access/authorizations": "/v1/authorizations", "/api/access/approval": "/v1/human/status",
		"/api/settings/domains": "/v1/domains", "/api/settings/secrets": "/v1/secrets", "/api/settings/doctor": "/v1/doctor", "/api/deployments/my-app/workflows": "/v1/workflows/my-app",

		"/api/deployments": "/v1/apps", "/api/operations": "/v1/status", "/api/deployments/my-app": "/v1/apps/my-app", "/api/deployments/my-app/logs?component=worker": "/v1/apps/my-app/logs?component=worker", "/api/operations/op-1": "/v1/operations/op-1", "/api/operations/op-1/events": "/v1/logs?operationId=op-1", "/api/observability?signal=metrics&app=my-app&limit=20": "/v1/telemetry/history?app=my-app&limit=20&signal=metrics",
	} {
		response := uiCall(s, "GET", local, "", s.token, "")
		// Every supported read should succeed through the authenticated local contract.
		if response.Code != 200 {
			t.Fatalf("read %s: %s", local, response.Body.String())
		}
		remote.mu.Lock()
		last := remote.paths[len(remote.paths)-1]
		remote.mu.Unlock()
		// Verify that controller paths cannot be replaced by browser input.
		if last != "GET "+upstream {
			t.Fatalf("%s mapped to %s", local, last)
		}
	}
	remote.mu.Lock()
	before := len(remote.paths)
	remote.mu.Unlock()
	// Invalid identities, numeric bounds and unrelated endpoints never reach the remote client.
	for _, path := range []string{"/api/activity?from=invalid", "/api/activity?app=a%2Fb", "/api/activity?from=2026-11-02T12:00:00Z&to=2026-11-01T12:00:00Z", "/api/activity?app=a&app=b", "/api/observability?signal=logs&beforeTime=2026-11-01T11:00:00Z", "/api/observability?signal=logs&level=invalid", "/api/tunnels/demo/connect", "/api/tunnels/a%2Fb", "/api/secrets", "/api/recovery/receipt/export", "/api/resources/cache/connection", "/api/settings/secrets/password", "/api/hosts/worker/proxy/secrets", "/api/deployments/a%2Fb", "/api/observability?signal=traces", "/api/observability?signal=logs&limit=201", "/api/observability?signal=logs&before=-1", "/api/observability?signal=metrics&component=..", "/api/deployments/a/logs?component=a%2Fb"} {
		// Malformed selectors must fail before authenticated network work.
		if response := uiCall(s, "GET", path, "", s.token, ""); response.Code != 400 {
			t.Fatalf("invalid %s: %d", path, response.Code)
		}
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	// Invalid requests cannot change the remote call inventory.
	if len(remote.paths) != before {
		t.Fatal("invalid read reached upstream")
	}
}

// TestPageReadCoalescingAndCapacity keeps a tab flood from growing the upstream query count or worker pool.
func TestPageReadCoalescingAndCapacity(t *testing.T) {
	remote := &pageRemote{started: make(chan string, 16), release: make(chan struct{}), value: "one"}
	s, _ := New(remote, "http://127.0.0.1:7777")
	var group sync.WaitGroup
	// Multiple tabs opening one history selector share a single pending read and immutable response.
	for range 12 {
		// Each tab waits independently on the shared immutable cache entry.
		group.Go(func() {
			response := uiCall(s, "GET", "/api/observability?signal=metrics", "", s.token, "")
			// Every joined viewer should receive the same successful result.
			if response.Code != 200 {
				t.Errorf("shared response: %s", response.Body.String())
			}
		})
	}
	<-remote.started
	// Other selectors use only the remaining bounded worker capacity.
	for _, name := range []string{"a", "b", "c"} {
		// Occupy another worker with a distinct controller selector.
		group.Go(func() { uiCall(s, "GET", "/api/deployments/"+name, "", s.token, "") })
		<-remote.started
	}
	response := uiCall(s, "GET", "/api/deployments/overflow", "", s.token, "")
	// A fifth unique read must fail admission without creating another worker.
	if response.Code != 400 || !strings.Contains(response.Body.String(), "RATE_LIMITED") {
		t.Fatalf("capacity was not bounded: %s", response.Body.String())
	}
	close(remote.release)
	group.Wait()
	// The complete tab flood uses only four upstream calls.
	if remote.calls.Load() != 4 {
		t.Fatalf("got %d upstream calls", remote.calls.Load())
	}
	uiCall(s, "GET", "/api/observability?signal=metrics", "", s.token, "")
	// A fresh result must be reused after the original viewers finish.
	if remote.calls.Load() != 4 {
		t.Fatal("fresh shared result was not cached")
	}
}

// TestPageCacheCannotCrossControllers verifies an old in-flight query cannot populate a new controller's view.
func TestPageCacheCannotCrossControllers(t *testing.T) {
	old := &pageRemote{started: make(chan string, 1), release: make(chan struct{}), value: "old"}
	next := &pageRemote{value: "new"}
	manager := &ManagedRemote{remote: old, generation: 1}
	s, _ := New(manager, "http://127.0.0.1:7777")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- uiCall(s, "GET", "/api/deployments/demo", "", s.token, "") }() // Hold the original controller read while selection changes.
	<-old.started
	manager.mu.Lock()
	manager.remote = next
	manager.generation++
	manager.mu.Unlock()
	response := uiCall(s, "GET", "/api/deployments/demo", "", s.token, "")
	// A new controller selection must have a distinct response and cache key.
	if !strings.Contains(response.Body.String(), `"controller":"new"`) {
		t.Fatalf("new view: %s", response.Body.String())
	}
	close(old.release)
	<-done
	response = uiCall(s, "GET", "/api/deployments/demo", "", s.token, "")
	// Late completion cannot replace the new controller cache.
	if !strings.Contains(response.Body.String(), `"controller":"new"`) {
		t.Fatal("old query overwrote the selected controller cache")
	}
}

// TestReadWorkerCancellation stops diagnostic work with the CLI instead of leaking a worker after shutdown.
func TestReadWorkerCancellation(t *testing.T) {
	remote := &pageRemote{started: make(chan string, 1), release: make(chan struct{})}
	s, _ := New(remote, "http://127.0.0.1:7777")
	ctx, cancel := context.WithCancel(context.Background())
	s.readContext = ctx
	done := make(chan struct{})
	// Keep the browser waiting while the CLI lifetime is canceled.
	go func() {
		defer close(done)
		r := httptest.NewRequest(http.MethodGet, s.origin+"/api/deployments/demo", nil)
		s.readPage(r)
	}() // The worker receives the CLI lifetime even when a browser remains open.
	<-remote.started
	cancel()
	// Worker shutdown must finish within a short bounded interval.
	select {
	// A prompt return confirms the pending worker honored cancellation.
	case <-done:
	// A blocked worker after cancellation would leak on CLI shutdown.
	case <-time.After(time.Second):
		t.Fatal("diagnostic worker ignored shutdown")
	}
}
