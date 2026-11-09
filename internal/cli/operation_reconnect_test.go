package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/client"
)

// reconnectController simulates temporary observation failures after one durable deployment acceptance.
type reconnectController struct {
	approvalBody map[string]any
	plans        atomic.Int32
	applies      atomic.Int32
	streams      atomic.Int32
	invalid      atomic.Bool
}

// ServeHTTP exposes plan, acceptance and reconnectable events through a real TLS connection.
func (c *reconnectController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Select the fixture behavior required to exercise this boundary.
	if r.Method == "POST" && r.URL.Path == "/v1/approvals" {
		var request struct {
			Body map[string]any `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		c.approvalBody = request.Body
		json.NewEncoder(w).Encode(map[string]string{"id": "fixture", "status": "approved"})
		return
	}
	// Select the fixture behavior required to exercise this boundary.
	if r.Method == "POST" && r.URL.Path == "/v1/approvals/fixture/execute" {
		raw, _ := json.Marshal(c.approvalBody)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.URL.Path = "/v1/plans/plan/apply"
	}

	// Every observation retry must keep its original authentication and operation identity.
	if r.Header.Get("Authorization") != "Bearer fixture-token" {
		c.invalid.Store(true)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Reconnecting can only repeat the event GET, never either deployment mutation.
	switch r.Method + " " + r.URL.Path {
	// Planning is performed once before the interactive approval.
	case "POST /v1/plans":
		c.plans.Add(1)
		fmt.Fprint(w, `{"id":"plan","fingerprint":"approved"}`)
	// Applying returns the durable identity before any connection failure.
	case "POST /v1/plans/plan/apply":
		c.applies.Add(1)
		fmt.Fprint(w, `{"operationId":"job"}`)
	// Only progress observation is affected by this fixture's failures.
	case "GET /v1/operations/job/events":
		c.events(w, r)
	// Unexpected polling or mutations fail rather than concealing a broken default path.
	default:
		c.invalid.Store(true)
		http.NotFound(w, r)
	}
}

// events rotates a stream, returns a transient gateway failure, then replays progress and completion.
func (c *reconnectController) events(w http.ResponseWriter, r *http.Request) {
	attempt := c.streams.Add(1)
	cursor := "job:7"
	// The first stream begins without requiring a user-provided resume cursor.
	if attempt == 1 {
		cursor = ""
	}
	// Later requests must resume from the last complete delivered event, never the partial frame.
	if r.Header.Get("Last-Event-ID") != cursor {
		c.invalid.Store(true)
	}
	// A temporary gateway outage must remain inside the same CLI observation loop.
	if attempt == 2 {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: log\nid: job:7\ndata: {\"operationId\":\"job\",\"sequence\":7,\"message\":\"Provisioning\"}\n\n")
	// Disconnect midway through the next frame to exercise safe automatic replay.
	if attempt == 1 {
		fmt.Fprint(w, "event: log\nid: job:8\ndata: {\"operationId\":")
		return
	}
	fmt.Fprint(w, "event: log\nid: job:8\ndata: {\"operationId\":\"job\",\"sequence\":8,\"message\":\"Ready\"}\n\n")
	fmt.Fprint(w, "event: operation\ndata: {\"id\":\"job\",\"status\":\"succeeded\"}\n\n")
}

// TestDeployAutomaticallyReconnects verifies ordinary deploy needs no manual watch command after interruptions.
func TestDeployAutomaticallyReconnects(t *testing.T) {
	fixture := &reconnectController{}
	server := httptest.NewTLSServer(fixture)
	defer server.Close()
	remote := &client.Client{URL: server.URL, HTTP: server.Client(), Token: "fixture-token"}
	var stdout, stderr bytes.Buffer
	approvals := 0
	options := Options{Client: remote, Home: t.TempDir(), CWD: project(t)}
	// Exactly one explicit approval authorizes the deployment before observation begins.
	options.Prompt = func(string, bool) (string, error) { approvals++; return "yes", nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := RunWithOptions(ctx, []string{"deploy"}, strings.NewReader(""), &stdout, &stderr, options)
	// The original deploy invocation must stay alive through both observation failures.
	if err != nil || !strings.Contains(stdout.String(), `"status":"succeeded"`) {
		t.Fatal("automatic observation did not reach completion", err)
	}
	// Reconnect uses reads only and preserves the original operation and approval.
	if fixture.invalid.Load() || fixture.plans.Load() != 1 || fixture.applies.Load() != 1 || approvals != 1 || fixture.streams.Load() != 3 {
		t.Fatal("reconnect changed deployment authority or lost its cursor")
	}
	assertReconnectOutput(t, stdout.String(), stderr.String())
}

// assertReconnectOutput verifies replay acknowledgement and distinct progress/result channels.
func assertReconnectOutput(t *testing.T, stdout, stderr string) {
	t.Helper()
	// Already delivered logs are suppressed while the incomplete frame is replayed once.
	if strings.Count(stderr, `"id":"job:7"`) != 1 || strings.Count(stderr, `"id":"job:8"`) != 1 {
		t.Fatal("reconnect duplicated or lost progress")
	}
	// Progress remains separate from the single authoritative terminal result.
	if strings.Count(stdout, `"status":"succeeded"`) != 1 || strings.Contains(stdout, "Provisioning") {
		t.Fatal("reconnect duplicated completion or polluted command output")
	}
}
