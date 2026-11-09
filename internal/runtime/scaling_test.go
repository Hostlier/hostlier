package runtime

import (
	"encoding/json"
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestReplicaCapabilities rejects unsupported layouts before providers are invoked.
func TestReplicaCapabilities(t *testing.T) {
	count := 3
	spec := model.Project{Replicas: &count}
	// Multiple Docker containers are outside its scheduling capability.
	if validateReplicas(spec, "docker") == nil {
		t.Fatal("Docker multi-replica accepted")
	}
	// Stateless k3s workloads support multiple ready instances.
	if err := validateReplicas(spec, "k3s"); err != nil {
		t.Fatal(err)
	}
	spec.Volumes = map[string]model.Volume{"data": {Mount: "/data", Size: "1Gi"}}
	// ReadWriteOnce is not a multi-writer concurrency capability.
	if validateReplicas(spec, "k3s") == nil {
		t.Fatal("persistent multiple writers accepted")
	}
}

// TestK3sReplicaManifest verifies explicit zero and positive desired counts reach the workload.
func TestK3sReplicaManifest(t *testing.T) {
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir()})
	// Manifest construction must not require a running cluster.
	if err != nil {
		t.Fatal(err)
	}
	k := adapter.(*K3s)
	// Both zero and greater-than-one counts must remain explicit in provider manifests.
	for _, count := range []int{0, 3} {
		spec := model.Project{Name: "app", Image: "nginx@sha256:" + strings.Repeat("a", 64), Port: 80, Replicas: &count}
		manifests, err := k.K3sManifests(spec, model.DeployContext{OperationID: "replica-plan"})
		// Serialization preserves the actual provider representation under test.
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(manifests)
		expected := `"replicas":0`
		// Build the distinct expected positive representation without aliasing test intent.
		if count == 3 {
			expected = `"replicas":3`
		}
		// The generated Deployment must carry the approved count verbatim.
		if !strings.Contains(string(raw), expected) {
			t.Fatal(string(raw))
		}
	}
}

// TestPendingSourceIdentity survives a replay after durable candidate activation.
func TestPendingSourceIdentity(t *testing.T) {
	d := dockerDeployment{operation: "accepted", old: appRecord{OperationID: "accepted", PreviousRuntimeID: "source", Deployment: model.Deployment{RuntimeID: "candidate"}}}
	// Replaying the committed operation must drain the source rather than the active candidate.
	if d.previousID() != "source" {
		t.Fatal(d.previousID())
	}
	d.operation = "next"
	// A distinct approved operation uses current active capacity as its source.
	if d.previousID() != "candidate" {
		t.Fatal(d.previousID())
	}
}
