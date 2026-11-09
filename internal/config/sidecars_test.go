package config

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
	"hostlier/internal/model"
)

// TestGenericSidecarExample accepts ordinary service and worker images without an agent profile.
func TestGenericSidecarExample(t *testing.T) {
	raw, err := os.ReadFile("../../examples/sidecars.hostlier")
	// The published example must remain valid input to the same deployment schema.
	if err != nil {
		t.Fatal(err)
	}
	var project model.Project
	// Decode the real example rather than silently replacing its fields in the test.
	if err = yaml.Unmarshal(raw, &project); err != nil {
		t.Fatal(err)
	}
	p, err := ValidateProject(project)
	// Neither sidecar needs Harnest, a provider key, or agent-specific metadata.
	if err != nil {
		t.Fatal(err)
	}
	backend, worker := p.Sidecars["backend"], p.Sidecars["worker"]
	// Ordinary sidecars retain their authored workload, source, and endpoint contract.
	if backend.Workload != "service" || backend.Port != 8081 || backend.Image != "busybox:1.37.0" {
		t.Fatal("generic backend was rewritten", backend)
	}
	assertGenericWorkerSidecar(t, worker)
}

// assertGenericWorkerSidecar verifies a sibling worker remains a listenerless ordinary process.
func assertGenericWorkerSidecar(t *testing.T, worker model.Project) {
	t.Helper()
	// A listenerless sidecar must not acquire a port or public route during normalization.
	if worker.Workload != "worker" || worker.Port != 0 || worker.Route != "" {
		t.Fatal("worker acquired service semantics", worker)
	}
}
