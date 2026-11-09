package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestPendingDrainFailureRetainsSourceUntilApprovedRetry proves failed drain is recoverable without losing its marker.
func TestPendingDrainFailureRetainsSourceUntilApprovedRetry(t *testing.T) {
	// Independent HTTPS readiness is required before the injected source stop is admitted.
	k := lifecycleFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	s, _ := k.base.read()
	app := s.Apps["app"]
	app.Status, app.PreviousRuntimeID = "running", "source"
	s.Apps["app"] = app
	// Commit the precise crash-recovery boundary before invoking the next approved attempt.
	if err := k.base.save(s); err != nil {
		t.Fatal(err)
	}
	d := &k3sDeployment{runtime: k, ctx: context.Background(), spec: app.Spec, s: s, old: app, namespace: k.namespace("app")}
	k.options.KubeRun = drainClusterFixture(k, true, false)
	// A failed source shutdown cannot authorize losing its durable ownership marker.
	if err := d.recoverDrain(); err == nil {
		t.Fatal("failed drain reported success")
	}
	persisted, _ := k.base.read()
	// Restarting the runtime still discovers the exact source awaiting shutdown.
	if persisted.Apps["app"].PreviousRuntimeID != "source" {
		t.Fatal("pending source lost")
	}
	k.options.KubeRun = drainClusterFixture(k, false, false)
	// A later approved attempt can finish the previous drain and continue its own deployment.
	if err := d.recoverDrain(); err != nil {
		t.Fatal(err)
	}
	persisted, _ = k.base.read()
	// Clear the marker only after the source has stopped and state committed.
	if persisted.Apps["app"].PreviousRuntimeID != "" || d.old.PreviousRuntimeID != "" {
		t.Fatal("successful drain did not commit")
	}
}

// TestPendingDrainRejectsChangedSourceOwnership blocks retiring a foreign replacement of the source object.
func TestPendingDrainRejectsChangedSourceOwnership(t *testing.T) {
	// The committed candidate remains healthy while source ownership is independently checked.
	k := lifecycleFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	s, _ := k.base.read()
	app := s.Apps["app"]
	app.PreviousRuntimeID = "source"
	s.Apps["app"] = app
	k.options.KubeRun = drainClusterFixture(k, false, true)
	d := &k3sDeployment{runtime: k, ctx: context.Background(), spec: app.Spec, s: s, old: app, namespace: k.namespace("app")}
	// A source that no longer belongs to this app must never be scaled or considered absent.
	if err := d.recoverDrain(); !hasCode(err, "OWNERSHIP") {
		t.Fatal("foreign source accepted", err)
	}
}

// drainClusterFixture models owned workload lookup and a failing source scale with no unrelated mutations.
func drainClusterFixture(k *K3s, fail, foreign bool) func(context.Context, []string, []byte) ([]byte, error) {
	// Observe the exact command path through the ordinary Kubernetes adapter.
	return func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		command := strings.Join(args, " ")
		// Deployment lookups expose actual owner labels independently of retained metadata.
		if strings.Contains(command, "get deployment") {
			labels := k.base.labels("app")
			// Simulate an administrator replacing only the source with another owner's workload.
			if foreign && strings.Contains(command, "get deployment source") {
				labels["io.hostlier.owner"] = "foreign"
			}
			return json.Marshal(map[string]any{"metadata": map[string]any{"labels": labels}})
		}
		// Both ownership rejection and stop failure must leave durable source identity intact.
		if strings.Contains(command, "scale deployment") && (fail || foreign) {
			return nil, errors.New("source stop unavailable")
		}
		return nil, nil
	}
}
