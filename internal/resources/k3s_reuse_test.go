package resources

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"hostlier/internal/model"
)

// selectorReuseCase distinguishes recognized historical layouts from unsafe ownership or selector drift.
type selectorReuseCase struct {
	name                 string
	role, drift, unowned bool
	wantCode             string
}

// TestK3sEngineReusePreservesImmutableSelectors covers both released layouts and rejects unsafe adoption.
func TestK3sEngineReusePreservesImmutableSelectors(t *testing.T) {
	// Exercise the historical selector, current engine selector and both independent safety boundaries.
	for _, test := range []selectorReuseCase{{name: "legacy"}, {name: "current", role: true}, {name: "selector drift", drift: true, wantCode: "RESOURCE_SELECTOR_DRIFT"}, {name: "unowned", unowned: true, wantCode: "RESOURCE_OWNERSHIP"}} {
		// Keep failures scoped to the exact compatibility or ownership boundary under test.
		t.Run(test.name, func(t *testing.T) { checkEngineSelectorReuse(t, test) })
	}
}

// checkEngineSelectorReuse models Kubernetes' immutable-selector rule while inspecting the actual apply payload.
func checkEngineSelectorReuse(t *testing.T, test selectorReuseCase) {
	m := &K3sManager{owner: "fixture-owner"}
	existing := engineSelectorFixture(m, test)
	applied := false
	// Return a real decoded ownership record and reject any selector change just as Kubernetes does.
	m.options.KubeRun = func(_ context.Context, args []string, input []byte) ([]byte, error) {
		return selectorReuseCommand(t, args, input, existing, &applied)
	}
	manifest := resourceEngineDeployment(m)
	err := m.apply(context.Background(), manifest, "cache")
	// Unsafe ownership or selectors must fail before a mutating apply reaches Kubernetes.
	if test.wantCode != "" {
		assertSelectorReuseRejected(t, err, applied, test.wantCode)
		return
	}
	// Recognized deployments must be reusable without changing their immutable selection boundary.
	if err != nil || !applied {
		t.Fatalf("recognized selector reuse failed: %v, applied=%v", err, applied)
	}
	template := manifest["spec"].(map[string]any)["template"].(map[string]any)
	labels := template["metadata"].(map[string]any)["labels"].(map[string]string)
	// The pod role remains mandatory even when the immutable historical selector does not include it.
	if labels["io.hostlier.role"] != "engine" {
		t.Fatal("legacy compatibility removed engine pod role")
	}
}

// engineSelectorFixture represents only recognized legacy/current selectors unless the case requests drift.
func engineSelectorFixture(m *K3sManager, test selectorReuseCase) map[string]any {
	labels := m.labels("cache")
	selectorLabels := m.labels("cache")
	// Current installations include an engine-role selector; earlier installations select stable ownership labels.
	if test.role {
		selectorLabels["io.hostlier.role"] = "engine"
	}
	// An additional selector cannot be adopted merely because the Deployment metadata is owned.
	if test.drift {
		selectorLabels["external-selector"] = "unexpected"
	}
	// A valid selector never substitutes for object-level server ownership.
	if test.unowned {
		labels["io.hostlier.owner"] = "different-owner"
	}
	metadataLabels := resourceDeploymentSelector(labels)["matchLabels"]
	return map[string]any{"metadata": map[string]any{"labels": metadataLabels}, "spec": map[string]any{"selector": resourceDeploymentSelector(selectorLabels)}}
}

// resourceEngineDeployment selects the real generated Deployment so tests cover its role-bearing pod template.
func resourceEngineDeployment(m *K3sManager) map[string]any {
	// Generate the same Redis resource objects that ordinary controller provisioning reapplies.
	for _, manifest := range m.manifests(model.ResourceSpec{Name: "cache", Type: "redis", Storage: "1Gi", Image: Images["redis"]}, model.ResourceCredentials{}) {
		// Other resource kinds do not have an immutable Deployment selector.
		if manifest["kind"] == "Deployment" {
			return manifest
		}
	}
	panic("engine manifest missing")
}

// selectorReuseCommand emulates read and server-side apply while enforcing Kubernetes selector immutability.
func selectorReuseCommand(t *testing.T, args []string, input []byte, existing map[string]any, applied *bool) ([]byte, error) {
	// A resource lookup returns the existing object before any adoption decision.
	if args[4] == "get" {
		return json.Marshal(existing)
	}
	var desired map[string]any
	// Decode the submitted wire representation before comparing immutable fields.
	if err := json.Unmarshal(input, &desired); err != nil {
		t.Fatal(err)
	}
	*applied = true
	oldSelector := existing["spec"].(map[string]any)["selector"]
	newSelector := desired["spec"].(map[string]any)["selector"]
	// Kubernetes rejects replacing even a subset selector with a role-augmented equivalent.
	if !reflect.DeepEqual(oldSelector, newSelector) {
		return nil, errors.New("immutable Deployment selector changed")
	}
	return nil, nil
}

// assertSelectorReuseRejected requires both the expected stable error code and absence of an apply mutation.
func assertSelectorReuseRejected(t *testing.T, err error, applied bool, wantCode string) {
	// Reject drift or unowned objects before sending an apply payload to the cluster.
	if !code(err, wantCode) || applied {
		t.Fatalf("unsafe selector adoption: error=%v, applied=%v", err, applied)
	}
}
