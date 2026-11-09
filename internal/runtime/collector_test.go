package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"hostlier/internal/collector"
	"hostlier/internal/model"
)

// managedCollectorSpec uses the same renderer and pinned registry origin as the controller.
func managedCollectorSpec(t *testing.T, backend string) model.CollectorDeployment {
	t.Helper()
	endpoint := "https://controller.example.com"
	image := "controller.example.com/hostlier/collector@sha256:" + strings.Repeat("a", 64)
	bundle, err := collector.Render(collector.Options{Runtime: backend, Endpoint: endpoint, Image: image})
	// A malformed fixture must not silently weaken runtime reconciliation coverage.
	if err != nil {
		t.Fatal(err)
	}
	return model.CollectorDeployment{Endpoint: endpoint, Image: image, Token: "fixture-token", Config: bundle.Config, Manifest: bundle.Deployment}
}

// collectorKubeFixture retains API objects and counts actual apply requests.
type collectorKubeFixture struct {
	objects map[string]map[string]any
	writes  int
	ready   bool
}

// run models Kubernetes defaults and current-generation DaemonSet readiness.
func (f *collectorKubeFixture) run(_ context.Context, args []string, input []byte) ([]byte, error) {
	// Apply records the exact desired object and generates server-owned status fields.
	if args[4] == "apply" {
		var object map[string]any
		// Test fixtures must accept only valid JSON emitted by the production adapter.
		if err := json.Unmarshal(input, &object); err != nil {
			return nil, err
		}
		f.writes++
		kind, name, namespace := collectorObjectIdentity(object)
		// Ready status is independent from successful object creation.
		if kind == "DaemonSet" {
			objectMap(object, "metadata")["generation"] = float64(f.writes)
			status := map[string]any{"observedGeneration": float64(f.writes), "desiredNumberScheduled": float64(2), "updatedNumberScheduled": float64(2), "numberReady": float64(2), "numberAvailable": float64(2)}
			// Simulate a node that has not become ready for the current generation.
			if !f.ready {
				status["numberReady"] = float64(1)
			} // One unavailable node must prevent success.
			object["status"] = status
		}
		f.objects[kind+"/"+namespace+"/"+name] = object
		return []byte("{}"), nil
	}
	namespace := ""
	// Namespace arguments remain explicit for all namespaced lookups.
	for i, arg := range args {
		// Use the explicit namespace passed by the production Kubernetes adapter.
		if arg == "-n" {
			namespace = args[i+1]
		}
	}
	object := f.objects[args[5]+"/"+namespace+"/"+args[6]]
	// Empty ignore-not-found responses distinguish missing resources from errors.
	if object == nil {
		return nil, nil
	}
	return json.Marshal(object)
}

// collectorKubeRuntime creates a completely isolated cluster command boundary.
func collectorKubeRuntime(t *testing.T) (*K3s, *collectorKubeFixture) {
	t.Helper()
	fixture := &collectorKubeFixture{objects: map[string]map[string]any{}, ready: true}
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir(), KubeRun: fixture.run})
	// Constructor failures must not produce a partially initialized test runtime.
	if err != nil {
		t.Fatal(err)
	}
	return adapter.(*K3s), fixture
}

// TestManagedCollectorK3sIdempotenceRotationRepair verifies singleton creation and stable reconciliation.
func TestManagedCollectorK3sIdempotenceRotationRepair(t *testing.T) {
	k, f := collectorKubeRuntime(t)
	spec := managedCollectorSpec(t, "k3s")
	// First ensure creates the six public objects plus the two scoped Secrets.
	if err := k.EnsureCollector(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	// Initial reconciliation must create each of the eight expected objects once.
	if f.writes != 8 {
		t.Fatalf("initial writes=%d", f.writes)
	} // Every singleton is created exactly once.
	// Repeating the same desired state must perform no apply or rollout.
	if err := k.EnsureCollector(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	// An unchanged request must not generate a new apply or rollout.
	if f.writes != 8 {
		t.Fatal("unchanged reconciliation wrote objects")
	}
	before := f.objects["DaemonSet/"+collectorNamespace+"/"+collectorName]["metadata"].(map[string]any)["annotations"].(map[string]any)[collectorHashLabel]
	spec.Token = "rotated-fixture-token"
	// Credential rotation changes the rollout fingerprint and Secret data together.
	if err := k.EnsureCollector(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	after := f.objects["DaemonSet/"+collectorNamespace+"/"+collectorName]["metadata"].(map[string]any)["annotations"].(map[string]any)[collectorHashLabel]
	// Rotating the credential must also rotate the DaemonSet template identity.
	if before == after {
		t.Fatal("rotation did not change rollout identity")
	}
	writes := f.writes
	delete(f.objects, "Secret/"+collectorNamespace+"/hostlier-telemetry")
	// A deleted Secret is repaired without recreating the existing DaemonSet.
	if err := k.EnsureCollector(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	// Repair only the missing Secret while preserving the existing workload.
	if f.writes != writes+1 {
		t.Fatal("repair changed unrelated resources")
	}
}

// TestManagedCollectorK3sForeignCollisionPreflight rejects adoption before any partial mutation.
func TestManagedCollectorK3sForeignCollisionPreflight(t *testing.T) {
	k, f := collectorKubeRuntime(t)
	f.objects["DaemonSet/"+collectorNamespace+"/"+collectorName] = map[string]any{"metadata": map[string]any{"labels": map[string]any{"io.hostlier.owner": "other"}}}
	err := k.EnsureCollector(context.Background(), managedCollectorSpec(t, "k3s"))
	// A collision at the final workload object must still prevent earlier Secret creation.
	if err == nil || f.writes != 0 {
		t.Fatalf("collision accepted: %v writes=%d", err, f.writes)
	}
}

// TestManagedCollectorK3sReadiness requires all desired nodes to observe the new rollout.
func TestManagedCollectorK3sReadiness(t *testing.T) {
	k, f := collectorKubeRuntime(t)
	f.ready = false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := k.EnsureCollector(ctx, managedCollectorSpec(t, "k3s"))
	// Successful apply is not enough while one desired node remains unready.
	if err == nil {
		t.Fatal("unready DaemonSet reported ready")
	}
}

// TestManagedCollectorDockerReuseAndCollision verifies unchanged processes are reused and foreign ones rejected.
func TestManagedCollectorDockerReuseAndCollision(t *testing.T) {
	spec := managedCollectorSpec(t, "docker")
	p, err := prepareCollector(spec)
	// Fingerprint construction must succeed before the matching daemon fixture is built.
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{owner: "fixture", options: Options{StateDir: t.TempDir()}}
	writes := 0
	labels := map[string]string{"io.hostlier.owner": r.owner, "io.hostlier.collector": "true", collectorHashLabel: p.revision}
	r.docker = &http.Client{Transport: sidecarTransport(func(request *http.Request) (*http.Response, error) { // All mutations are counted independently from daemon inspection.
		if request.Method != "GET" {
			writes++
		}
		return sidecarResponse(map[string]any{"Id": "collector-id", "Config": map[string]any{"Image": spec.Image, "Labels": labels}, "State": map[string]bool{"Running": true}})
	})}
	// An already matching running process must need neither pull nor restart.
	if err = r.EnsureCollector(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	// Matching running collectors require no Docker mutations.
	if writes != 0 {
		t.Fatal("unchanged collector was mutated")
	}
	labels["io.hostlier.owner"] = "foreign"
	// Foreign ownership at the deterministic name must fail closed.
	if err = r.EnsureCollector(context.Background(), spec); err == nil {
		t.Fatal("adopted foreign collector")
	}
	// A rejected foreign singleton must remain entirely untouched.
	if writes != 0 {
		t.Fatal("foreign collector was mutated")
	}
}
