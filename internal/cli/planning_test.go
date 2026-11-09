package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// planningReconnectFixture drops acceptance and observation replies after durable acceptance.
type planningReconnectFixture struct {
	approvalBody map[string]any
	mu           sync.Mutex
	requestID    string
	posts        atomic.Int32
	polls        atomic.Int32
	applies      atomic.Int32
	completed    atomic.Bool
	invalid      atomic.Bool
}

// ServeHTTP preserves the original preparation identity across real connection failures.
func (f *planningReconnectFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Select the fixture behavior required to exercise this boundary.
	if f.approvalFixture(w, r) {
		return
	}

	// Verify the expected preparation and approval boundary.
	if isPlanningStart(r) {
		f.accept(w, r)
		return
	}
	// Verify the expected preparation and approval boundary.
	if r.Method == "GET" && r.URL.Path == "/v1/planning/job" {
		f.poll(w)
		return
	}
	// Verify the expected preparation and approval boundary.
	if r.Method == "POST" && r.URL.Path == "/v1/plans/review/apply" {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		// Verify the expected preparation and approval boundary.
		if !f.completed.Load() || body["fingerprint"] != "exact-review" || body["approved"] != true {
			f.invalid.Store(true)
		}
		f.applies.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"operationId": "deployment"})
		return
	}
	f.invalid.Store(true)
	http.NotFound(w, r)
}

// accept validates immutable idempotency when the first accepted response is lost.
func (f *planningReconnectFixture) accept(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	id, _ := body["requestId"].(string)
	f.mu.Lock()
	// Verify the expected preparation and approval boundary.
	if f.requestID == "" {
		f.requestID = id
	}
	// Verify the expected preparation and approval boundary.
	if id == "" || id != f.requestID {
		f.invalid.Store(true)
	}
	f.mu.Unlock()
	// Verify the expected preparation and approval boundary.
	if f.posts.Add(1) == 1 {
		dropPlanningConnection(w)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"id": "job", "status": "queued"})
}

// dropPlanningConnection closes the transport before delivering a complete response.
func dropPlanningConnection(w http.ResponseWriter) {
	connection, _, err := w.(http.Hijacker).Hijack()
	// Verify the expected preparation and approval boundary.
	if err == nil {
		connection.Close()
	}
}

// TestWorkflowPlanningReconnectRequiresExactApproval exercises the real HTTPS client and CLI review.
func TestWorkflowPlanningReconnectRequiresExactApproval(t *testing.T) {
	fixture := &planningReconnectFixture{}
	server := httptest.NewTLSServer(fixture)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	approvals := 0
	// Exercise each controlled fixture path without real provider mutations.
	s := &session{ctx: ctx, remote: &client.Client{URL: server.URL, HTTP: server.Client()}, out: &stdout, errOut: &stderr, interactive: true, flags: map[string]string{}, options: Options{PollInterval: time.Millisecond, Prompt: func(string, bool) (string, error) {
		approvals++
		// Verify the expected preparation and approval boundary.
		if !fixture.completed.Load() {
			t.Error("approval preceded completed review")
		}
		return "yes", nil
	}}}
	flow := planFlow{path: "/v1/plans", body: map[string]any{"spec": model.Project{Workflow: &workflow.Spec{}}}, prompt: "Approve?"}
	receipt, err := s.reviewPlanAttempt(flow)
	// Verify the expected preparation and approval boundary.
	if err != nil || receipt.OperationID != "deployment" {
		t.Fatal(receipt, err)
	}
	// Verify the expected preparation and approval boundary.
	if fixture.posts.Load() != 2 || fixture.polls.Load() != 4 || fixture.applies.Load() != 1 || approvals != 1 || fixture.invalid.Load() {
		t.Fatal("reconnect changed planning or approval identity")
	}
	// Verify the expected preparation and approval boundary.
	if !strings.Contains(stderr.String(), "no deployment approved") {
		t.Fatal("preparation progress missing")
	}
}

// TestNonworkflowPlansRemainSynchronous preserves compatibility with existing controller endpoints.
func TestNonworkflowPlansRemainSynchronous(t *testing.T) {
	// Verify the expected preparation and approval boundary.
	if asynchronousPlan(planFlow{path: "/v1/plans", body: map[string]any{"spec": model.Project{}}}) {
		t.Fatal("legacy plan became asynchronous")
	}
	// Verify the expected preparation and approval boundary.
	if asynchronousPlan(planFlow{path: "/v1/resource-plans", body: map[string]any{"spec": model.Project{Workflow: &workflow.Spec{}}}}) {
		t.Fatal("resource route changed")
	}
}

// poll emits reconnectable preparation progress independently of deployment execution.
func (f *planningReconnectFixture) poll(w http.ResponseWriter) {
	poll := f.polls.Add(1)
	// Simulate enough disconnects to exhaust one ordinary request retry.
	if poll <= 2 {
		dropPlanningConnection(w)
		return
	}
	// Verify the expected preparation and approval boundary.
	if poll == 3 {
		json.NewEncoder(w).Encode(map[string]any{"id": "job", "status": "running"})
		return
	}
	f.completed.Store(true)
	json.NewEncoder(w).Encode(map[string]any{"id": "job", "status": "completed", "plan": map[string]any{"id": "review", "fingerprint": "exact-review"}})
	return
}

// approvalFixture translates test-only signed intent into the existing planning stream fixture.
func (f *planningReconnectFixture) approvalFixture(w http.ResponseWriter, r *http.Request) bool {
	// Select the fixture behavior required to exercise this boundary.
	if r.Method == "POST" && r.URL.Path == "/v1/approvals" {
		var request struct {
			Body map[string]any `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		f.approvalBody = request.Body
		json.NewEncoder(w).Encode(map[string]string{"id": "fixture", "status": "approved"})
		return true
	}
	// Select the fixture behavior required to exercise this boundary.
	if r.Method == "POST" && r.URL.Path == "/v1/approvals/fixture/execute" {
		raw, _ := json.Marshal(f.approvalBody)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.URL.Path = "/v1/plans/review/apply"
	}

	return false
}

// isPlanningStart selects only preparation requests in the reconnect fixture.
func isPlanningStart(r *http.Request) bool { return r.Method == "POST" && r.URL.Path == "/v1/planning" }
