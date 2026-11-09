package controller

import (
	"bytes"
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

	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// gatedPlanner simulates long provider inspection while detecting unauthorized application.
type gatedPlanner struct {
	started chan struct{}
	release chan struct{}
	plans   atomic.Int32
	applies atomic.Int32
}

// Plan waits for controlled provider completion or the administrator's bounded deadline.
func (g *gatedPlanner) Plan(ctx context.Context, _ workflow.Request) (workflow.Prepared, error) {
	g.plans.Add(1)
	// Exercise each controlled fixture path without real provider mutations.
	select {
	// Exercise each controlled fixture path without real provider mutations.
	case g.started <- struct{}{}:
	// Exercise each controlled fixture path without real provider mutations.
	default:
	}
	// Exercise each controlled fixture path without real provider mutations.
	select {
	// Exercise each controlled fixture path without real provider mutations.
	case <-g.release:
		return workflow.Prepared{Digest: strings.Repeat("a", 64), Summary: []string{"one reviewed change"}, Payload: json.RawMessage(`{"private":"prepared"}`)}, nil
	// Exercise each controlled fixture path without real provider mutations.
	case <-ctx.Done():
		return workflow.Prepared{}, ctx.Err()
	}
}

// Apply records forbidden eager provisioning during preparation-only tests.
func (g *gatedPlanner) Apply(context.Context, workflow.Request, string) (workflow.Result, error) {
	g.applies.Add(1)
	return workflow.Result{}, nil
}

// planningController creates an authenticated real HTTP fixture with controlled provider latency.
func planningController(t *testing.T, dir string, planner *gatedPlanner, timeout time.Duration) (*Controller, *httptest.Server, string) {
	t.Helper()
	c, err := New(Options{StateDir: dir, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: &recordingRuntime{}, Packages: planner, PlanningTimeout: timeout})
	// Verify the expected preparation and approval boundary.
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	credentials := c.credentials("planner")
	c.mu.Unlock()
	server := httptest.NewServer(c.Handler())
	return c, server, credentials["token"].(string)
}

// planningHTTP executes real network requests without sharing handler execution lifetime.
func planningHTTP(t *testing.T, server *httptest.Server, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	data, err := json.Marshal(body)
	// Verify the expected preparation and approval boundary.
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
	// Verify the expected preparation and approval boundary.
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(request)
	// Verify the expected preparation and approval boundary.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result map[string]any
	// Verify the expected preparation and approval boundary.
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, result
}

// planningIntent supplies private desired values that must be encrypted at rest.
func planningIntent(id string) map[string]any {
	spec := project()
	spec.Workflow = &workflow.Spec{Steps: []workflow.Step{{ID: "infra", Package: "fixture", Config: map[string]any{"private": "planning-config-sentinel"}}}}
	return map[string]any{"requestId": id, "request": map[string]any{"spec": spec}}
}

// awaitPlanning observes completion through the public job route within a short test bound.
func awaitPlanning(t *testing.T, server *httptest.Server, token, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	// Exercise each controlled fixture path without real provider mutations.
	for time.Now().Before(deadline) {
		_, job := planningHTTP(t, server, token, "GET", "/v1/planning/"+id, nil)
		// Verify the expected preparation and approval boundary.
		if job["status"] != "queued" && job["status"] != "running" {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("planning did not finish")
	return nil
}

// TestPlanningHTTPReconnectAndApproval keeps long preparation independent of disconnected callers.
func TestPlanningHTTPReconnectAndApproval(t *testing.T) {
	planner := &gatedPlanner{started: make(chan struct{}, 1), release: make(chan struct{})}
	dir := t.TempDir()
	c, server, token := planningController(t, dir, planner, time.Second)
	defer closeController(t, c)
	defer server.Close()
	body := planningIntent("same-request")
	_, accepted := planningHTTP(t, server, token, "POST", "/v1/planning", body)
	id := accepted["id"].(string)
	<-planner.started
	// New TCP connections recover the same accepted job without repeating provider inspection.
	server.Client().CloseIdleConnections()
	_, repeated := planningHTTP(t, server, token, "POST", "/v1/planning", body)
	// Verify the expected preparation and approval boundary.
	if repeated["id"] != id {
		t.Fatal("reconnect duplicated planning")
	}
	c.mu.Lock()
	other := c.credentials("other-planner")
	c.mu.Unlock()
	_, hidden := planningHTTP(t, server, other["token"].(string), "GET", "/v1/planning/"+id, nil)
	// Another authenticated actor cannot read this job.
	if code(hidden) != "NOT_FOUND" {
		t.Fatal(hidden)
	}
	c.mu.Lock()
	ciphertext := c.state.PlanningJobs[id].Intent
	c.mu.Unlock()
	// Verify the expected preparation and approval boundary.
	if strings.Contains(ciphertext, "planning-config-sentinel") {
		t.Fatal("plaintext planning intent persisted")
	}
	assertPlanningEncryption(t, dir)
	close(planner.release)
	job := awaitPlanning(t, server, token, id)
	// Verify the expected preparation and approval boundary.
	if job["status"] != "completed" {
		t.Fatal(job)
	}
	plan := job["plan"].(map[string]any)
	// Plan completion and receipt polling alone can never authorize provisioning.
	if planner.plans.Load() != 1 || planner.applies.Load() != 0 {
		t.Fatal("preparation changed provisioning authority")
	}
	_, denied := planningHTTP(t, server, token, "POST", "/v1/plans/"+plan["id"].(string)+"/apply", map[string]any{"approved": false, "fingerprint": plan["fingerprint"], "requestId": "denied"})
	// Verify the expected preparation and approval boundary.
	if code(denied) != "HUMAN_APPROVAL_REQUIRED" || planner.applies.Load() != 0 {
		t.Fatal(denied)
	}
	// Conflicting reuse cannot redirect an existing durable request identity.
	conflict := planningIntent("same-request")
	conflict["request"] = map[string]any{"spec": project()}
	_, denied = planningHTTP(t, server, token, "POST", "/v1/planning", conflict)
	// Verify the expected preparation and approval boundary.
	if code(denied) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(denied)
	}

}

// TestPlanningGenerationAndTimeout rejects snapshots crossed by mutations and bounds slow providers.
func TestPlanningGenerationAndTimeout(t *testing.T) {
	// Exercise each controlled fixture path without real provider mutations.
	for _, scenario := range []string{"generation", "timeout"} {
		// Exercise each controlled fixture path without real provider mutations.
		t.Run(scenario, func(t *testing.T) {
			planner := &gatedPlanner{started: make(chan struct{}, 1), release: make(chan struct{})}
			timeout := time.Second
			// Verify the expected preparation and approval boundary.
			if scenario == "timeout" {
				timeout = 30 * time.Millisecond
			}
			c, server, token := planningController(t, t.TempDir(), planner, timeout)
			defer closeController(t, c)
			defer server.Close()
			_, accepted := planningHTTP(t, server, token, "POST", "/v1/planning", planningIntent(scenario))
			<-planner.started
			// Verify the expected preparation and approval boundary.
			if scenario == "generation" {
				c.mu.Lock()
				c.state.Generation++
				c.mu.Unlock()
				close(planner.release)
			}
			job := awaitPlanning(t, server, token, accepted["id"].(string))
			// Verify the expected preparation and approval boundary.
			if job["status"] != "failed" || job["plan"] != nil || planner.applies.Load() != 0 {
				t.Fatal(job)
			}
			// Verify the expected preparation and approval boundary.
			if scenario == "generation" && code(job) != "STALE_PLAN" {
				t.Fatal(job)
			}
		})
	}
}

// TestPlanningRestartNeverReplays marks durable unfinished preparations for explicit operator retry.
func TestPlanningRestartNeverReplays(t *testing.T) {
	dir := t.TempDir()
	planner := &gatedPlanner{started: make(chan struct{}, 1), release: make(chan struct{})}
	c, server, token := planningController(t, dir, planner, time.Second)
	c.mu.Lock()
	actor := ""
	// Exercise each controlled fixture path without real provider mutations.
	for id := range c.state.Clients {
		actor = id
	}
	sealed, _ := c.store.Encrypt(`{"spec":{"private":"restart-sentinel"}}`)
	c.state.PlanningJobs["interrupted"] = &PlanningJob{ID: "interrupted", Actor: actor, Status: "running", RequestID: "restart", Intent: sealed}
	c.store.Save(c.state)
	c.mu.Unlock()
	server.Close()
	closeController(t, c)
	// Opening the same durable state recovers the job without calling any provider action.
	restarted := newTestController(t, dir, &recordingRuntime{}, time.Now)
	defer closeController(t, restarted)
	reopened := httptest.NewServer(restarted.Handler())
	defer reopened.Close()
	_, job := planningHTTP(t, reopened, token, "GET", "/v1/planning/interrupted", nil)
	// Verify the expected preparation and approval boundary.
	if job["status"] != "interrupted" || code(job) != "RECOVERY_REQUIRED" || planner.plans.Load() != 0 || planner.applies.Load() != 0 {
		t.Fatal(job)
	}
	response, err := reopened.Client().Get(reopened.URL + "/health")
	// Verify the expected preparation and approval boundary.
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
}

// assertPlanningEncryption inspects actual database and WAL bytes while the private intent is pending.
func assertPlanningEncryption(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	// Exercise each controlled fixture path without real provider mutations.
	for _, entry := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
		// Verify the expected preparation and approval boundary.
		if strings.Contains(string(data), "planning-config-sentinel") {
			t.Fatal("plaintext pending intent in durable state")
		}
	}
}
