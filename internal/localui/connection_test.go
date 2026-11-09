package localui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/client"
)

// pairingFixture exercises the real verified client transport without exposing private receipts to the browser.
type pairingFixture struct {
	approved atomic.Bool
	begins   atomic.Int32
	polls    atomic.Int32
}

// ServeHTTP implements existing enrollment and snapshot contracts for the link integration test.
func (f *pairingFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Enrollment never requires or receives an existing controller credential.
	switch r.URL.Path {
	// The public challenge contains a private polling secret retained by the local process.
	case "/v1/pairing/requests":
		f.begins.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"requestId": "request-private-id", "requestSecret": "polling-private-secret", "code": "0123456789abcdef", "expiresAt": time.Now().Add(time.Minute).UnixMilli()})
	// Only this original private polling request may consume approved enrollment.
	case "/v1/pairing/poll":
		f.polls.Add(1)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		// A public approval code alone is insufficient to retrieve credentials.
		if body["requestSecret"] != "polling-private-secret" {
			w.WriteHeader(403)
			return
		}
		// Unapproved pairing cannot return an authenticated identity.
		if !f.approved.Load() {
			io.WriteString(w, `{"status":"pending"}`)
			return
		} // Simulate a distinct administrator approving the public code.
		io.WriteString(w, `{"status":"complete","clientId":"paired-client","token":"controller-private-token"}`)
	// The selected credential is required on authenticated reads after enrollment.
	case "/v1/status":
		// Only the selected saved credential may read authenticated controller data.
		if r.Header.Get("Authorization") != "Bearer controller-private-token" {
			w.WriteHeader(401)
			return
		} // Tokens never transit the browser.
		io.WriteString(w, `{"hostname":"linked.example.com"}`)
	// All other fixture routes fail closed instead of acting as a generic proxy.
	default:
		w.WriteHeader(404)
	}
}

// linkFixture starts an empty profile store and a TLS-verified enrollment service.
func linkFixture(t *testing.T) (*Server, *ManagedRemote, *pairingFixture, *httptest.Server) {
	t.Helper()
	fixture := &pairingFixture{}
	upstream := httptest.NewTLSServer(fixture)
	t.Cleanup(upstream.Close) // No test listener survives a failed enrollment assertion.
	manager, err := NewManagedRemote(client.NewStore(t.TempDir()), "")
	// A new UI must start without a saved controller or credential file.
	if err != nil {
		t.Fatal(err)
	}
	// Trust the fixture certificate while using the real production request client.
	manager.factory = func(options client.Options) (Remote, error) {
		return &client.Client{URL: options.URL, Token: options.Token, HTTP: upstream.Client()}, nil
	} // The injected HTTP client trusts only the fixture certificate.
	local, err := New(manager, "http://127.0.0.1:7777")
	// Session generation is required before any browser-like local API request.
	if err != nil {
		t.Fatal(err)
	}
	return local, manager, fixture, upstream
}

// TestConnectionLinksAndSavesCLIProfile verifies first-run enrollment through the same local API used by the browser.
func TestConnectionLinksAndSavesCLIProfile(t *testing.T) {
	local, manager, fixture, upstream := linkFixture(t)
	body, _ := json.Marshal(map[string]string{"name": "vm", "url": upstream.URL})
	response := uiCall(local, "POST", "/api/connection/link", string(body), local.token, local.origin)
	// Browser-visible state contains only the approval locator, not enrollment or controller secrets.
	if response.Code != 200 || strings.Contains(response.Body.String(), "private") {
		t.Fatal(response.Code, response.Body.String())
	}
	state, _ := manager.State()
	// A challenge alone must never become an authenticated selected connection.
	if state.Pending == nil || state.Name != "" {
		t.Fatal("unapproved enrollment selected a controller")
	}
	response = uiCall(local, "POST", "/api/connection/poll", `{}`, local.token, local.origin)
	// Unapproved enrollment leaves the saved profile store empty.
	if response.Code != 200 || fixture.begins.Load() != 1 {
		t.Fatal("pending link failed")
	}
	fixture.approved.Store(true)
	response = uiCall(local, "POST", "/api/connection/poll", `{}`, local.token, local.origin)
	// Independent approval is required before credentials can be persisted for CLI use.
	if response.Code != 200 || strings.Contains(response.Body.String(), "controller-private-token") {
		t.Fatal(response.Code, response.Body.String())
	}
	assertSavedLink(t, local, manager, fixture, upstream.URL)
}

// TestConnectionRejectsForeignRequestsAndConflictingApprovals prevents local API authority confusion.
func TestConnectionRejectsForeignRequestsAndConflictingApprovals(t *testing.T) {
	local, _, fixture, upstream := linkFixture(t)
	body, _ := json.Marshal(map[string]string{"name": "vm", "url": upstream.URL})
	response := uiCall(local, "POST", "/api/connection/link", string(body), local.token, "https://foreign.example")
	// A malicious page cannot initiate pairing through a local CLI credential bridge.
	if response.Code != 403 || fixture.begins.Load() != 0 {
		t.Fatal("foreign request allocated pairing")
	}
	response = uiCall(local, "POST", "/api/connection/link", `{"name":"vm","url":"http://insecure.example"}`, local.token, local.origin)
	// Certificate verification cannot be bypassed through user-entered URL schemes.
	if response.Code == 200 || fixture.begins.Load() != 0 {
		t.Fatal("non-HTTPS pairing accepted")
	}
	local.approvals["original-controller-approval"] = true
	response = uiCall(local, "POST", "/api/connection/select", `{"name":"vm"}`, local.token, local.origin)
	// Pending exact-request authorization cannot be transferred to another controller.
	if response.Code == 200 || !strings.Contains(response.Body.String(), "APPROVAL_PENDING") {
		t.Fatal("approval authority crossed controllers")
	}
}

// trackedRemote records observer concurrency while synthetic controller selection changes.
type trackedRemote struct {
	uiRemote
	active, maximum *atomic.Int32
}

// WatchStatus keeps one selected observer live until its owner cancels it.
func (r *trackedRemote) WatchStatus(ctx context.Context, consume func(client.StatusEvent) error) error {
	active := r.active.Add(1)
	defer r.active.Add(-1)
	// Atomic maximum tracking detects overlapping subscriptions across connection changes.
	for previous := r.maximum.Load(); active > previous; previous = r.maximum.Load() {
		// Retry maximum tracking if another admission changed it concurrently.
		if r.maximum.CompareAndSwap(previous, active) {
			break
		}
	} // Concurrent old/new watches would violate the single-upstream guarantee.
	return r.uiRemote.WatchStatus(ctx, consume)
}

// TestConnectionSelectionJoinsPreviousWatch verifies switching resets data and never overlaps upstream observers.
func TestConnectionSelectionJoinsPreviousWatch(t *testing.T) {
	store := client.NewStore(t.TempDir())
	// Saved profiles model ordinary prior CLI enrollment rather than browser-side credentials.
	for _, name := range []string{"first", "second"} {
		// Every selected name must identify a real saved CLI profile.
		if err := store.Save(name, "https://"+name+".example", "client", name); err != nil {
			t.Fatal(err)
		}
	} // Each profile keeps its own credential binding.
	manager, err := NewManagedRemote(store, "")
	// Profile metadata must remain readable before constructing any live transport worker.
	if err != nil {
		t.Fatal(err)
	}
	var active, maximum atomic.Int32
	factory := func(client.Options) (Remote, error) { return &trackedRemote{active: &active, maximum: &maximum}, nil } // Test transports share admission counters only.
	manager.factory = factory
	// Use the controlled fixture transport before starting a watch.
	if err = manager.Select("first"); err != nil {
		t.Fatal(err)
	} // Start observation on a controlled, cancellable transport.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	samples := make(chan client.StatusEvent, 16)
	done := make(chan error, 1)
	// The connection owner runs independently of the test driver.
	go func() {
		// Capture reset and observation events without introducing browser polling.
		done <- manager.WatchStatus(ctx, func(event client.StatusEvent) error { samples <- event; return nil })
	}() // The manager owns switching and joining this observer.
	waitConnectionSample(t, samples, false)
	// Switch to the second enrolled identity while the original watch is live.
	if err = manager.Select("second"); err != nil {
		t.Fatal(err)
	} // A replacement must cancel and join the earlier subscription.
	waitConnectionSample(t, samples, true)
	waitConnectionSample(t, samples, false)
	cancel()
	<-done
	// No old observation goroutine or concurrent upstream stream can remain after cancellation.
	if active.Load() != 0 || maximum.Load() != 1 {
		t.Fatalf("active=%d max=%d", active.Load(), maximum.Load())
	}
}

// waitConnectionSample waits for reset or data without relying on scheduling sleeps.
func waitConnectionSample(t *testing.T, samples <-chan client.StatusEvent, reset bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	// Context-bound notifications make switching regressions fail promptly rather than deadlock.
	for {
		// Bound every notification wait by a wall-clock test deadline.
		select {
		// Ignore other transport state while waiting for the requested connection boundary.
		case event := <-samples:
			// Resets and complete observations are separate connection boundaries.
			if (reset && event.Reset) || (!reset && event.Snapshot != nil) {
				return
			} // A full observation is distinct from a cache-reset marker.
		// Failure indicates the connection owner did not publish its state transition.
		case <-deadline.C:
			t.Fatal("connection sample timeout")
		}
	}
}

// assertSavedLink verifies CLI persistence, credential privacy and authenticated post-link requests.
func assertSavedLink(t *testing.T, local *Server, manager *ManagedRemote, fixture *pairingFixture, expectedURL string) {
	t.Helper()
	options, err := manager.store.Get("vm")
	// The ordinary CLI reads the same persisted profile and credential, with no second pairing flow.
	if err != nil || options.Token != "controller-private-token" || options.URL != expectedURL {
		t.Fatal("profile was not saved for CLI use", err)
	}
	info, err := os.Stat(filepath.Join(manager.store.Home, "credentials.json"))
	// Locally persisted credentials retain the CLI's owner-only file contract.
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential permissions", err)
	}
	var status map[string]any
	err = manager.Request(context.Background(), "GET", "/v1/status", nil, &status)
	// The active dashboard uses the saved authenticated transport to reach the linked controller.
	if err != nil || status["hostname"] != "linked.example.com" {
		t.Fatal("linked controller inaccessible", err)
	}
	previous := fixture.polls.Load()
	uiCall(local, "POST", "/api/connection/poll", `{}`, local.token, local.origin)
	// Repeated browser refreshes must not reconsume enrollment or allocate another credential.
	if fixture.polls.Load() != previous {
		t.Fatal("completed link consumed enrollment twice")
	}
}
