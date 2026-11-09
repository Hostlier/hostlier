package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hostlier/internal/model"
	"hostlier/internal/store"
)

// TestStatusRestartRequiresCurrentEpoch prevents retained broker data from advertising current readiness.
func TestStatusRestartRequiresCurrentEpoch(t *testing.T) {
	directory := t.TempDir()
	first := newTestController(t, directory, &recordingRuntime{}, nil)
	first.captureStatus()
	retained := awaitCapturedStatus(t, first)
	closeController(t, first)
	reopened := newTestController(t, directory, &recordingRuntime{}, nil)
	defer closeController(t, reopened)
	// The real file-backed stream recovered its latest snapshot before workers start.
	if reopened.observations.bus.Latest() != retained {
		t.Fatal("JetStream did not recover retained telemetry")
	}
	var snapshot struct {
		ID         string `json:"id"`
		ObservedAt string `json:"observedAt"`
	}
	json.Unmarshal([]byte(reopened.latestStatus()), &snapshot)
	// Cached telemetry from an older process must be cold desired metadata for a newly attached CLI.
	if snapshot.ID != reopened.observations.epoch+":0" || snapshot.ObservedAt != "0001-01-01T00:00:00Z" {
		t.Fatal("old process telemetry appeared current", snapshot)
	}
	reopened.captureStatus()
	awaitCapturedStatus(t, reopened)
	json.Unmarshal([]byte(reopened.latestStatus()), &snapshot)
	// A fresh capture can replace cold metadata only after its broker-backed delivery succeeds.
	if snapshot.ID != reopened.observations.epoch+":1" {
		t.Fatal("new process capture unavailable", snapshot)
	}
}

// TestStatusBrokerStartupFailureReleasesControllerOwnership verifies partial initialization is recoverable.
func TestStatusBrokerStartupFailureReleasesControllerOwnership(t *testing.T) {
	directory := t.TempDir()
	// A regular file blocks the broker directory without changing durable controller data.
	if err := os.WriteFile(filepath.Join(directory, "observations"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{StateDir: directory, Runtime: &recordingRuntime{}, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}}
	_, err := New(options)
	// Startup cannot silently claim telemetry readiness when its private broker cannot open storage.
	if err == nil {
		t.Fatal("controller started without observation broker")
	}
	var state State
	reopened, err := store.Open(directory, &state)
	// Failed startup must release SQLite ownership instead of stranding the server's writer lock.
	if err != nil {
		t.Fatal("failed broker startup retained controller ownership", err)
	}
	reopened.Close()
}

// TestStatusBrokerFailureLeavesAcceptedOperationsAvailable keeps telemetry outside deployment authority.
func TestStatusBrokerFailureLeavesAcceptedOperationsAvailable(t *testing.T) {
	c := observationController(t, &recordingRuntime{})
	broker, ok := c.observations.bus.(interface{ Close() }) // Production uses the real embedded broker.
	// An unowned test bus would fail to exercise a real broker shutdown.
	if !ok {
		t.Fatal("controller did not start embedded JetStream")
	}
	broker.Close()
	c.captureStatus()
	assertResponsiveHealth(t, c)
	status, result := call(t, c, true, "POST", "/v1/apps/sample/action", map[string]any{"action": "restart", "purge": false}, "")
	// Approved local execution must still durably accept work when telemetry has stopped.
	if status != 200 {
		t.Fatal("telemetry failure blocked operation acceptance", result)
	}
	operation := wait(t, c, result["operationId"].(string))
	// The existing durable executor has no dependency on delivery worker acknowledgements.
	if operation["status"] != "succeeded" {
		t.Fatal("telemetry failure blocked operation execution", operation)
	}
}
