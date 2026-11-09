package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/model"
)

// observationRuntime exposes blocked diagnostics while tracking simultaneous probe workers.
type observationRuntime struct {
	recordingRuntime
	gate    chan struct{}
	started chan struct{}
	calls   atomic.Int32
	active  atomic.Int32
	maximum atomic.Int32
}

// Status blocks only the observation worker and includes private fields to exercise allowlisting.
func (r *observationRuntime) Status(ctx context.Context, _ string) (map[string]any, error) {
	r.calls.Add(1)
	active := r.active.Add(1)
	defer r.active.Add(-1)
	// Track the highest admitted worker count without serializing the probes themselves.
	for previous := r.maximum.Load(); active > previous; previous = r.maximum.Load() {
		// Retry only when another worker updated the shared maximum first.
		if r.maximum.CompareAndSwap(previous, active) {
			break
		}
	}
	// Buffered starts let the test observe admission without slowing the runtime fixture.
	select {
	// Record admission without requiring the test to drain starts immediately.
	case r.started <- struct{}{}:
	// A full signal buffer must never delay runtime work.
	default:
	}
	// A test gate simulates Docker or Kubernetes hanging until the supplied context ends.
	select {
	// Runtime output includes data that must never reach the shared stream.
	case <-r.gate:
		return map[string]any{"running": true, "readyReplicas": 1, "environment": "PRIVATE_OBSERVATION_VALUE", "status": "PRIVATE_OBSERVATION_VALUE"}, nil
	// Probe timeout and controller shutdown interrupt the worker.
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// observationController initializes an independent controller with a cold on-demand sampler.
func observationController(t *testing.T, r model.Runtime) *Controller {
	t.Helper()
	c, err := New(Options{StateDir: t.TempDir(), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: r, StatusInterval: time.Hour, StatusTimeout: time.Second})
	// A fixture without durable initialization cannot exercise controller invariants.
	if err != nil {
		t.Fatal(err)
	}
	// Shut down controller-owned background workers after every fixture.
	t.Cleanup(func() { closeController(t, c) })
	c.mu.Lock()
	c.state.Applications["sample"] = &Application{Deployment: model.Deployment{Name: "sample", Status: "running"}, Spec: project()}
	if err := c.store.Save(c.state); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	return c
}

// TestReadRequestsDoNotPersist proves SQL-backed reads leave the snapshot unchanged.
func TestReadRequestsDoNotPersist(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	before, err := c.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	// Health, inventories, diagnostics and status use no aggregate update at all.
	for _, path := range []string{"/health", "/v1/status", "/v1/apps", "/v1/doctor", "/v1/logs", "/v1/status/snapshot"} {
		status, out := call(t, c, true, "GET", path, nil, "")
		// Every collection query must remain available to a reader.
		if status != 200 {
			t.Fatalf("read %s attempted persistence: %v", path, out)
		}
	}
	after, err := c.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read changed persisted state")
	}
}

// TestBlockedRuntimeStatusDoesNotHoldControllerLock proves diagnostic I/O cannot block ordinary reads.
func TestBlockedRuntimeStatusDoesNotHoldControllerLock(t *testing.T) {
	r := &observationRuntime{gate: make(chan struct{}), started: make(chan struct{}, 8)}
	c := observationController(t, r)
	done := make(chan struct{})
	// A status inspection waits in the fake backend while the controller handles unrelated traffic.
	go func() {
		defer close(done)
		c.LocalHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/apps/sample", nil))
	}()
	awaitProbe(t, r.started)
	assertResponsiveHealth(t, c)
	close(r.gate)
	<-done
}

// awaitProbe bounds fixture waits so a worker-admission regression produces a useful failure.
func awaitProbe(t *testing.T, started <-chan struct{}) {
	t.Helper()
	// A missing start must fail without leaving the suite waiting indefinitely.
	select {
	// Continue once the expected worker entered the runtime.
	case <-started:
	// Bound worker admission waits even when production code regresses.
	case <-time.After(time.Second):
		t.Fatal("runtime probe never started")
	}
}

// assertResponsiveHealth measures controller admission while a runtime observation is blocked.
func assertResponsiveHealth(t *testing.T, c *Controller) {
	t.Helper()
	done := make(chan int, 1)
	// A separate goroutine makes the failure observable even if a handler incorrectly owns the lock.
	go func() {
		w := httptest.NewRecorder()
		c.LocalHandler().ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
		done <- w.Code
	}()
	// Runtime stalls must not serialize unrelated controller requests.
	select {
	// The unrelated health request should complete while runtime I/O remains blocked.
	case status := <-done:
		// A reachable controller must still report successful health.
		if status != http.StatusOK {
			t.Fatal(status)
		}
	// Fail if the diagnostic request retained the shared state lock.
	case <-time.After(300 * time.Millisecond):
		t.Fatal("runtime probe blocked controller health")
	}
}

// TestStatusCaptureSharesBoundedWorkersAndRedacts exercises the producer/fanout boundary with multiple viewers.
func TestStatusCaptureSharesBoundedWorkersAndRedacts(t *testing.T) {
	r := &observationRuntime{gate: make(chan struct{}), started: make(chan struct{}, 32)}
	c := observationController(t, r)
	c.mu.Lock()
	// More applications than workers establish a real bounded queue rather than one goroutine per app.
	for i := 0; i < 9; i++ {
		name := fmt.Sprintf("app-%d", i)
		c.state.Applications[name] = &Application{Deployment: model.Deployment{Name: name}, Spec: project()}
	}
	if err := c.store.Save(c.state); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	first, stopFirst := c.observations.bus.Subscribe()
	defer stopFirst()
	second, stopSecond := c.observations.bus.Subscribe()
	defer stopSecond()
	done := make(chan struct{})
	// Capture runs on a worker owner independently of viewer reads.
	go func() { defer close(done); c.captureStatus() }()
	// All four workers are occupied before additional jobs can be admitted.
	for range 4 {
		awaitProbe(t, r.started)
	}
	assertResponsiveHealth(t, c)
	close(r.gate)
	<-done
	<-first
	<-second
	// Each app was probed once even though two subscribers observed the result.
	if r.calls.Load() != 10 || r.maximum.Load() > 4 {
		t.Fatalf("calls=%d max workers=%d", r.calls.Load(), r.maximum.Load())
	}
	data := c.observations.bus.Latest()
	// Free-form provider text and project configuration are absent from serialized observations.
	if strings.Contains(data, "PRIVATE_OBSERVATION_VALUE") || strings.Contains(data, "secretRefs") {
		t.Fatal("observation leaked private runtime data")
	}
	var out model.StatusSnapshot
	// Replacement identity and completion time distinguish a sample from desired metadata.
	if json.Unmarshal([]byte(data), &out) != nil || out.ID == "" || out.ObservedAt.IsZero() {
		t.Fatal("incomplete observation")
	}
}

// TestStatusCaptureTimeoutReleasesWorkers proves hung probes honor bounded contexts.
func TestStatusCaptureTimeoutReleasesWorkers(t *testing.T) {
	r := &observationRuntime{gate: make(chan struct{}), started: make(chan struct{}, 8)}
	c := observationController(t, r)
	c.observations.timeout = 20 * time.Millisecond
	c.captureStatus()
	// Failed probes become explicit unavailable observations without retaining background workers.
	if r.active.Load() != 0 || !strings.Contains(awaitCapturedStatus(t, c), "Runtime observation unavailable") {
		t.Fatal("timed-out probe retained work or invented health")
	}
}

// mappedObservationRuntime returns the same native field shapes used by real Docker and Kubernetes adapters.
type mappedObservationRuntime struct {
	recordingRuntime
	diagnostics map[string]any
}

// Status returns immutable test diagnostics without converting Kubernetes JSON counters to Go ints.
func (r *mappedObservationRuntime) Status(context.Context, string) (map[string]any, error) {
	return r.diagnostics, nil
}

// TestStatusCaptureNormalizesRuntimeReadiness prevents Kubernetes replicas from appearing stopped or unknown.
func TestStatusCaptureNormalizesRuntimeReadiness(t *testing.T) {
	cases := []struct {
		name        string
		diagnostics map[string]any
		ready       int
	}{
		{"kubernetes", map[string]any{"readyReplicas": float64(3)}, 3},
		{"docker", map[string]any{"running": true, "components": []model.ComponentStatus{{Name: "app", Running: true, Ready: true}}}, 1},
	}
	// Exercise the actual capture pipeline for both supported backend result shapes.
	for _, item := range cases {
		// Independent controllers keep probe ownership and cached state isolated between adapters.
		t.Run(item.name, func(t *testing.T) {
			c := observationController(t, &mappedObservationRuntime{diagnostics: item.diagnostics})
			c.captureStatus()
			var snapshot model.StatusSnapshot
			// A completed capture must preserve the backend's observed readiness.
			if json.Unmarshal([]byte(awaitCapturedStatus(t, c)), &snapshot) != nil {
				t.Fatal("invalid capture")
			}
			app := snapshot.Applications[0]
			// Native number encoding must not hide observed running replicas.
			if !app.Running || app.ReadyReplicas == nil || *app.ReadyReplicas != item.ready {
				t.Fatalf("unexpected readiness: %+v", app)
			}
		})
	}
}

// awaitCapturedStatus waits for durable publication and consumer fanout of the just-completed capture.
func awaitCapturedStatus(t *testing.T, c *Controller) string {
	t.Helper()
	updates, unsubscribe := c.observations.bus.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	expected := fmt.Sprintf("%s:%d", c.observations.epoch, c.observations.sequence)
	// Queue admission alone does not make a capture visible to CLI viewers.
	for {
		value := c.observations.bus.Latest()
		var snapshot model.StatusSnapshot
		// A full replacement must traverse JetStream before the test accepts it.
		if json.Unmarshal([]byte(value), &snapshot) == nil && snapshot.ID == expected {
			return value
		}
		// A broker or consumer failure must produce a bounded test failure.
		select {
		// A completed cache update may now match the capture identity.
		case <-updates:
		// Preserve the expected identity when reporting publication failure.
		case <-ctx.Done():
			t.Fatalf("capture %s never reached fanout", expected)
		}
	}
}
