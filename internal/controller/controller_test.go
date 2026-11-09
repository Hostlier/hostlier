package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hostlier/internal/model"
)

// recordingRuntime captures deployment inputs and exposes controlled blocking or failure.
type recordingRuntime struct {
	mu       sync.Mutex
	calls    int
	values   map[string]string
	networks []string
	fail     bool
	gate     chan struct{}
}

// Name supports name in the integration fixture.
func (r *recordingRuntime) Name() string { return "docker" }

// InspectImage supports inspect image in the integration fixture.
func (r *recordingRuntime) InspectImage(context.Context, string) (model.ImageInfo, error) {
	return model.ImageInfo{Digest: "example/image@sha256:" + strings.Repeat("a", 64)}, nil
}

// Deploy supports deploy in the integration fixture.
func (r *recordingRuntime) Deploy(ctx context.Context, p model.Project, c model.DeployContext) (model.Deployment, error) {
	// Fail immediately if fixture setup or execution fails.
	if r.gate != nil {
		// Wait for completion or cancellation without blocking indefinitely.
		select {
		// Allow deployment when the fixture gate opens.
		case <-r.gate:
		// Stop gated fixture work when its context is cancelled.
		case <-ctx.Done():
			return model.Deployment{}, ctx.Err()
		}
	}
	// Fail immediately if fixture setup or execution fails.
	if c.OnEvent != nil {
		c.OnEvent(model.Event{Level: "info", Step: "health", Message: c.Secrets["KEY"]})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.values = c.Secrets
	r.networks = c.DependencyNetworks
	// Fail immediately if fixture setup or execution fails.
	if r.fail {
		return model.Deployment{}, errors.New(c.Secrets["KEY"])
	}
	return model.Deployment{RuntimeID: "recorded", ImageDigest: p.Image}, nil
}

// Action supports action in the integration fixture.
func (r *recordingRuntime) Action(context.Context, string, string, model.ActionContext) error {
	return nil
}

// Logs supports logs in the integration fixture.
func (r *recordingRuntime) Logs(context.Context, string) (string, error) { return "logs", nil }

// Doctor supports doctor in the integration fixture.
func (r *recordingRuntime) Doctor(context.Context) (any, error) {
	return map[string]bool{"ok": true}, nil
}

// newTestController supports new test controller in the integration fixture.
func newTestController(t *testing.T, dir string, r model.Runtime, now func() time.Time) *Controller {
	t.Helper()
	c, err := New(Options{StateDir: dir, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: r, Hostname: "hostlier.example.com", Now: now, PairingTTL: time.Minute})
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// testCredentials issues fixture credentials under the same state lock as API requests.
func testCredentials(c *Controller, name string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credentials(name)
}

// call supports call in the integration fixture.
func call(t *testing.T, c *Controller, local bool, method, path string, body any, token string) (int, map[string]any) {
	t.Helper()
	var data []byte
	// Fail immediately if fixture setup or execution fails.
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.RemoteAddr = "192.0.2.10:10000"
	// Fail immediately if fixture setup or execution fails.
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	// Fail immediately if fixture setup or execution fails.
	if local {
		c.LocalHandler().ServeHTTP(w, r)
	} else {
		c.Handler().ServeHTTP(w, r)
	}
	var result map[string]any
	// Guard against the expected result.
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	return w.Code, result
}

// project supports project in the integration fixture.
func project() model.Project {
	return model.Project{Version: 1, Name: "sample", Image: "example/image:1", Port: 3000, Route: "sample.example.com"}
}

// code supports code in the integration fixture.
func code(v map[string]any) string {
	e, _ := v["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// wait supports wait in the integration fixture.
func wait(t *testing.T, c *Controller, id string) map[string]any {
	t.Helper()
	// Real package subprocesses need time for race instrumentation and durable SQLite writes.
	deadline := time.Now().Add(20 * time.Second)
	// Poll within a wall-clock bound instead of assuming each API observation is instantaneous.
	for time.Now().Before(deadline) {
		_, o := call(t, c, true, "GET", "/v1/operations/"+id, nil, "")
		// Require the expected operation state.
		if o["status"] != "queued" && o["status"] != "running" {
			return o
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("operation timeout")
	return nil
}

// closeController supports close controller in the integration fixture.
func closeController(t *testing.T, c *Controller) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	// Fail immediately if fixture setup or execution fails.
	if err := c.Close(ctx); err != nil {
		t.Error(err)
	}
}

// TestPairingBothDirectionsRevocationExpiryAndRestart verifies pairing both directions revocation expiry and restart.
func TestPairingBothDirectionsRevocationExpiryAndRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	r := &recordingRuntime{}
	// Use the test clock to exercise durable enrollment expiry.
	c := newTestController(t, dir, r, func() time.Time { return now })
	status, _ := call(t, c, false, "GET", "/v1/status", nil, "")
	// Require the expected HTTP status for this request.
	if status != 401 {
		t.Fatal(status)
	}
	_, issued := call(t, c, true, "POST", "/v1/pairing/tokens", map[string]any{"name": "laptop"}, "")
	status, paired := call(t, c, false, "POST", "/v1/pairing/redeem", map[string]any{"name": "laptop", "token": issued["token"]}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(paired)
	}
	credential := paired["token"].(string)
	status, _ = call(t, c, false, "GET", "/v1/status", nil, credential)
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(status)
	}
	closeController(t, c)
	// Preserve the same test clock across controller restart.
	c = newTestController(t, dir, r, func() time.Time { return now })
	defer closeController(t, c)
	status, _ = call(t, c, false, "POST", "/v1/pairing/redeem", map[string]any{"name": "replay", "token": issued["token"]}, "")
	// Require the expected HTTP status for this request.
	if status != 403 {
		t.Fatal(status)
	}
	req := assertRequestedPairing(t, c)
	call(t, c, true, "POST", "/v1/clients/"+paired["clientId"].(string)+"/revoke", map[string]any{}, "")
	status, _ = call(t, c, false, "GET", "/v1/status", nil, credential)
	// Require the expected HTTP status for this request.
	if status != 401 {
		t.Fatal(status)
	}
	_, late := call(t, c, true, "POST", "/v1/pairing/tokens", map[string]any{"name": "late"}, "")
	now = now.Add(time.Hour)
	status, _ = call(t, c, false, "POST", "/v1/pairing/redeem", map[string]any{"name": "late", "token": late["token"]}, "")
	// Require the expected HTTP status for this request.
	if status != 403 {
		t.Fatal(status)
	}
	data := controllerSnapshot(t, c)
	// Guard against plaintext credential stored.
	for _, v := range []any{issued["token"], req["requestSecret"], paired["token"]} {
		// Guard against plaintext credential stored.
		if strings.Contains(string(data), v.(string)) {
			t.Fatal("plaintext credential stored")
		}
	}
}

// TestApprovalSecretsReplayRollbackAndFailureRedaction verifies approval secrets replay rollback and failure redaction.
func TestApprovalSecretsReplayRollbackAndFailureRedaction(t *testing.T) {
	dir := t.TempDir()
	r := &recordingRuntime{}
	c := newTestController(t, dir, r, nil)
	defer closeController(t, c)
	value := "private-multiline\nsecret"
	_, secret := call(t, c, true, "PUT", "/v1/secrets/key", map[string]any{"value": value, "requestId": "upload"}, "")
	_, retry := call(t, c, true, "PUT", "/v1/secrets/key", map[string]any{"value": value, "requestId": "upload"}, "")
	// Guard against upload replay rotated secret.
	if secret["revision"] != retry["revision"] {
		t.Fatal("upload replay rotated secret")
	}
	spec := project()
	spec.Secrets = map[string]string{"KEY": "key"}
	status, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	// Require the expected HTTP status for this request.
	if status != 200 {
		t.Fatal(plan)
	}
	r.mu.Lock()
	// Guard against plan mutated runtime.
	if r.calls != 0 {
		t.Fatal("plan mutated runtime")
	}
	r.mu.Unlock()
	path := "/v1/plans/" + plan["id"].(string) + "/apply"
	status, _ = call(t, c, true, "POST", path, map[string]any{"fingerprint": plan["fingerprint"], "requestId": "apply"}, "")
	// Require the expected HTTP status for this request.
	if status != 403 {
		t.Fatal(status)
	}
	body := map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": "apply"}
	_, accepted := call(t, c, true, "POST", path, body, "")
	op := wait(t, c, accepted["operationId"].(string))
	// Require the expected operation state.
	if op["status"] != "succeeded" {
		t.Fatal(op)
	}
	_, replayed := call(t, c, true, "POST", path, body, "")
	// Guard against duplicate apply.
	if replayed["operationId"] != accepted["operationId"] {
		t.Fatal("duplicate apply")
	}
	r.mu.Lock()
	// Guard against secret changed.
	if r.values["KEY"] != value {
		t.Fatal("secret changed")
	}
	r.mu.Unlock()
	assertRollbackAndFailureProtection(t, c, r, spec, accepted, secret, value, dir)
}

// TestStalePlansAndPendingRouteReservations verifies stale plans and pending route reservations.
func TestStalePlansAndPendingRouteReservations(t *testing.T) {
	r := &recordingRuntime{gate: make(chan struct{})}
	c := newTestController(t, t.TempDir(), r, nil)
	defer closeController(t, c)
	_, stale := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": project()}, "")
	call(t, c, true, "PUT", "/v1/secrets/key", map[string]any{"value": "x", "requestId": "s"}, "")
	status, _ := call(t, c, true, "POST", "/v1/plans/"+stale["id"].(string)+"/apply", map[string]any{"approved": true, "fingerprint": stale["fingerprint"], "requestId": "old"}, "")
	// Require the expected HTTP status for this request.
	if status != 409 {
		t.Fatal(status)
	}
	_, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": project()}, "")
	_, accepted := call(t, c, true, "POST", "/v1/plans/"+plan["id"].(string)+"/apply", map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": "fresh"}, "")
	other := project()
	other.Name = "other"
	status, out := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": other}, "")
	// Require the expected HTTP status for this request.
	if status != 409 || code(out) != "ROUTE_CONFLICT" {
		t.Fatal(status, out)
	}
	close(r.gate)
	wait(t, c, accepted["operationId"].(string))
}

// TestDualListenersUnixPrivilegesAndStrictBodies verifies dual listeners unix privileges and strict bodies.
func TestDualListenersUnixPrivilegesAndStrictBodies(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	socket := filepath.Join(t.TempDir(), "controller.sock")
	// Guard against local listener unavailable.
	if err := c.Listen(ListenOptions{SocketPath: socket, TCP: true}); err != nil {
		t.Skipf("local listener unavailable: %v", err)
	}
	response, err := http.Get("http://" + c.Address() + "/v1/status")
	// Fail immediately if fixture setup or execution fails.
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	// Fail immediately if fixture setup or execution fails.
	if response.StatusCode != 401 {
		t.Fatal(response.StatusCode)
	}
	status, _ := call(t, c, true, "POST", "/v1/pairing/tokens", map[string]any{"name": "x", "unknown": true}, "")
	// Require the expected HTTP status for this request.
	if status != 400 {
		t.Fatal(status)
	}
}

// TestInterruptedJobsAreActionableAndReplayNeverCreatesNewExecution verifies interrupted jobs are actionable and replay never creates new execution.
func TestInterruptedJobsAreActionableAndReplayNeverCreatesNewExecution(t *testing.T) {
	dir := t.TempDir()
	r := &recordingRuntime{}
	c := newTestController(t, dir, r, nil)
	c.mu.Lock()
	c.state.Operations["accepted"] = &Operation{ID: "accepted", Name: "sample", Kind: "deploy", Status: "running", Spec: project(), PlanID: "approved-plan", Fingerprint: "approved-fingerprint", Actor: "local"}
	// Fail immediately if fixture setup or execution fails.
	if err := c.store.Save(c.state); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	closeController(t, c)
	c = newTestController(t, dir, r, nil)
	defer closeController(t, c)
	_, op := call(t, c, true, "GET", "/v1/operations/accepted", nil, "")
	// Require the expected operation state.
	if op["status"] != "interrupted" || op["error"].(map[string]any)["code"] != "RECOVERY_REQUIRED" {
		t.Fatal(op)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Guard against unapproved recovery mutation.
	if r.calls != 0 {
		t.Fatal("unapproved recovery mutation")
	}
}

// TestEnrollmentRateLimitIgnoresSourcePortAndBodyLimits verifies enrollment rate limit ignores source port and body limits.
func TestEnrollmentRateLimitIgnoresSourcePortAndBodyLimits(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	// Exercise each fixture case before evaluating the result.
	for i := 0; i < 61; i++ {
		body := bytes.NewBufferString(`{"name":"request"}`)
		r := httptest.NewRequest("POST", "/v1/pairing/requests", body)
		r.RemoteAddr = fmt.Sprintf("192.0.2.99:%d", 10000+i)
		w := httptest.NewRecorder()
		c.Handler().ServeHTTP(w, r)
		// Guard against source port bypassed rate limiter.
		if i == 60 && w.Code != 429 {
			t.Fatal("source port bypassed rate limiter", w.Code)
		}
	}
	status, _ := call(t, c, true, "PUT", "/v1/secrets/key", map[string]any{"value": strings.Repeat("x", 131073), "requestId": "large"}, "")
	// Guard against oversized secret accepted.
	if status == 200 {
		t.Fatal("oversized secret accepted")
	}
}

// assertRequestedPairing checks that polling requires the request secret and returns approved credentials.
func assertRequestedPairing(t *testing.T, c *Controller) map[string]any {
	t.Helper()
	_, req := call(t, c, false, "POST", "/v1/pairing/requests", map[string]any{"name": "requested"}, "")
	call(t, c, true, "POST", "/v1/pairing/approve", map[string]any{"code": req["code"]}, "")
	status, _ := call(t, c, false, "POST", "/v1/pairing/poll", map[string]any{"requestId": req["requestId"], "requestSecret": req["code"]}, "")
	// Require the expected HTTP status for this request.
	if status != 403 {
		t.Fatal(status)
	}
	status, complete := call(t, c, false, "POST", "/v1/pairing/poll", map[string]any{"requestId": req["requestId"], "requestSecret": req["requestSecret"]}, "")
	// Require the expected HTTP status for this request.
	if status != 200 || complete["status"] != "complete" {
		t.Fatal(complete)
	}
	return req
}

// assertRollbackAndFailureProtection checks pinned rollback secrets and redaction after a failed deployment.
func assertRollbackAndFailureProtection(t *testing.T, c *Controller, r *recordingRuntime, spec model.Project, accepted, secret map[string]any, value, dir string) {
	t.Helper()
	call(t, c, true, "PUT", "/v1/secrets/key", map[string]any{"value": "rotated", "requestId": "rotate"}, "")
	_, rollback := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec, "action": "rollback", "revision": accepted["operationId"]}, "")
	refs := rollback["secretRefs"].(map[string]any)
	// Guard against rollback selected current secret.
	if refs["KEY"].(map[string]any)["revision"] != secret["revision"] {
		t.Fatal("rollback selected current secret")
	}
	status, _ := call(t, c, true, "DELETE", "/v1/secrets/key", nil, "")
	// Require the expected HTTP status for this request.
	if status != 409 {
		t.Fatal(status)
	}
	r.mu.Lock()
	r.fail = true
	r.mu.Unlock()
	_, failurePlan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	_, failure := call(t, c, true, "POST", "/v1/plans/"+failurePlan["id"].(string)+"/apply", map[string]any{"approved": true, "fingerprint": failurePlan["fingerprint"], "requestId": "fail"}, "")
	failed := wait(t, c, failure["operationId"].(string))
	// Require the expected operation state.
	if failed["status"] != "failed" {
		t.Fatal(failed)
	}
	raw, _ := json.Marshal(failed)
	// Guard against runtime error leaked secret.
	if strings.Contains(string(raw), value) || strings.Contains(string(raw), "rotated") {
		t.Fatal("runtime error leaked secret")
	}
	data := controllerSnapshot(t, c)
	// Guard against plaintext secret stored.
	if strings.Contains(string(data), value) || strings.Contains(string(data), "rotated") {
		t.Fatal("plaintext secret stored")
	}
}
