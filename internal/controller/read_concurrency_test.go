package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"hostlier/internal/model"
)

// blockedReadRuntime uses the same slow backend boundary for log and doctor requests.
type blockedReadRuntime struct {
	recordingRuntime
	gate    chan struct{}
	started chan struct{}
}

// Logs waits until the backend finishes without requiring authoritative state access.
func (r *blockedReadRuntime) Logs(ctx context.Context, _ string) (string, error) { return r.wait(ctx) }

// Doctor exercises the same runtime I/O boundary through a separate controller read route.
func (r *blockedReadRuntime) Doctor(ctx context.Context) (any, error) { return r.wait(ctx) }

// wait signals backend admission and honors cancellation while the controller serves other requests.
func (r *blockedReadRuntime) wait(ctx context.Context) (string, error) {
	r.started <- struct{}{}
	// A stalled runtime can only hold its own caller until completion or cancellation.
	select {
	// Release the fake external diagnostic call.
	case <-r.gate:
		return "diagnostic", nil
	// Shutdown and request cancellation terminate the backend call.
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestBlockedLogsAndDoctorLeaveControllerResponsive covers non-status reads that previously owned the lock during I/O.
func TestBlockedLogsAndDoctorLeaveControllerResponsive(t *testing.T) {
	// Each public runtime read must allow unrelated controller health requests to proceed.
	for _, path := range []string{"/v1/apps/sample/logs", "/v1/doctor"} {
		// A fresh gate prevents one diagnostic's completion from unblocking the next case.
		t.Run(path, func(t *testing.T) {
			r := &blockedReadRuntime{gate: make(chan struct{}), started: make(chan struct{}, 1)}
			c := observationController(t, r)
			done := make(chan struct{})
			// This request remains in runtime I/O while the test probes controller responsiveness.
			go func() {
				defer close(done)
				c.LocalHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
			}()
			awaitProbe(t, r.started)
			assertResponsiveHealth(t, c)
			close(r.gate)
			<-done
		})
	}
}

// stalledResponse isolates response writes from the authoritative state response snapshot.
type stalledResponse struct {
	header        http.Header
	started, gate chan struct{}
	once          sync.Once
}

// Header exposes only this response's independent header map.
func (w *stalledResponse) Header() http.Header { return w.header }

// WriteHeader deliberately performs no network work in this response fixture.
func (*stalledResponse) WriteHeader(int) {}

// Write simulates a slow reader after serialization has finished.
func (w *stalledResponse) Write(data []byte) (int, error) {
	// The test only needs to observe the first response-write admission.
	w.once.Do(func() { close(w.started) })
	<-w.gate
	return len(data), nil
}

// TestSlowResponseWriteDoesNotHoldStateLock catches state lock ownership across network delivery.
func TestSlowResponseWriteDoesNotHoldStateLock(t *testing.T) {
	c := observationController(t, &recordingRuntime{})
	w := &stalledResponse{header: make(http.Header), started: make(chan struct{}), gate: make(chan struct{})}
	done := make(chan struct{})
	// This response freezes mutable state and then stalls only its network writer.
	go func() {
		defer close(done)
		c.LocalHandler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/status", nil))
	}()
	awaitProbe(t, w.started)
	assertResponsiveHealth(t, c)
	close(w.gate)
	<-done
}

// blockedResources simulates slow resource observations while inventory membership changes concurrently.
type blockedResources struct {
	recordingResources
	read blockedReadRuntime
}

// Status delays external resource inspection using a cancellable backend fixture.
func (r *blockedResources) Status(ctx context.Context, name string) (model.Resource, error) {
	_, err := r.read.wait(ctx)
	return model.Resource{Name: name, Status: "running"}, err
}

// Logs verifies resource log inspection releases the same authoritative lock as application logs.
func (r *blockedResources) Logs(ctx context.Context, _ string) (string, error) {
	return r.read.wait(ctx)
}

// TestResourceInventoryConcurrentMutation permits resource reads and map updates during slow runtime I/O.
func TestResourceInventoryConcurrentMutation(t *testing.T) {
	manager := &blockedResources{read: blockedReadRuntime{gate: make(chan struct{}), started: make(chan struct{}, 8)}}
	c := observationController(t, &recordingRuntime{})
	c.mu.Lock()
	c.opts.ResourceManager = manager
	c.state.Resources["database"] = &ResourceRecord{Resource: model.Resource{Name: "database", Status: "running"}}
	if err := c.store.Save(c.state); err != nil { t.Fatal(err) }
	c.mu.Unlock()
	done := make(chan struct{})
	// Collection iteration must snapshot map membership before the first external call releases state.
	go func() {
		defer close(done)
		c.LocalHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/resources", nil))
	}()
	awaitProbe(t, manager.read.started)
	assertResponsiveHealth(t, c)
	c.mu.Lock()
	c.state.Resources["another"] = &ResourceRecord{Resource: model.Resource{Name: "another", Status: "running"}}
	delete(c.state.Resources, "database")
	c.mu.Unlock()
	close(manager.read.gate)
	<-done
}
