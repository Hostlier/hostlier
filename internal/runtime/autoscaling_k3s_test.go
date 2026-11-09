package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// hpaFixture simulates Kubernetes object identity and recoverable API failures.
type hpaFixture struct {
	runtime    *K3s
	objects    map[string]map[string]any
	deletes    int
	failDelete bool
	failApply  bool
	failGet    bool
}

// newHPAFixture supplies real durable runtime state with an isolated in-memory Kubernetes API.
func newHPAFixture(t *testing.T) (*hpaFixture, state) {
	t.Helper()
	f := &hpaFixture{objects: map[string]map[string]any{}}
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir(), KubeRun: f.run})
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = adapter.(*K3s)
	spec := model.Project{Version: 1, Name: "app", Image: "image@sha256:" + strings.Repeat("a", 64), Port: 80, Route: "app.example.com", Requests: &model.Limits{CPU: "0.01", Memory: "32Mi"}, Resources: &model.Limits{CPU: "0.1", Memory: "64Mi"}, Autoscaling: &model.Autoscaling{MinReplicas: 1, MaxReplicas: 3, CPUTargetPercent: 65}}
	app := appRecord{Deployment: model.Deployment{Name: "app", RuntimeID: "version", Status: "running"}, Spec: spec, OperationID: "approved", Autoscaling: &autoscalingRecord{Phase: "pending", Target: "version", UID: "target-uid", Template: config.Fingerprint(nil), Policy: config.Fingerprint(spec.Autoscaling)}}
	target := map[string]any{"metadata": f.runtime.metadata("app", "version")}
	target["metadata"].(map[string]any)["uid"] = "target-uid"
	raw, _ := json.Marshal(target)
	json.Unmarshal(raw, &target)
	f.objects["deployment/version"] = target
	s := state{Apps: map[string]appRecord{"app": app}}
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err = f.runtime.base.save(s); err != nil {
		t.Fatal(err)
	}
	return f, s
}

// run implements only the object commands needed by lifecycle tests, preserving JSON API shapes.
func (f *hpaFixture) run(_ context.Context, args []string, input []byte) ([]byte, error) {
	// Exercise each independent policy or recovery case with its own fixture state.
	for len(args) > 0 && !hpaFixtureCommand(args[0]) {
		args = args[1:]
	}
	// Select the one policy or ownership fault exercised by this isolated scenario.
	switch args[0] {
	// Read authoritative object identity without inventing absence on API failure.
	case "get":
		return f.get(args)
	// Install the exact reviewed HPA manifest unless the injected boundary rejects it.
	case "apply":
		// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
		if f.failApply {
			return nil, errors.New("injected apply failure")
		}
		var obj map[string]any
		// Require a successful fixture operation or authoritative observation before continuing safety assertions.
		if err := json.Unmarshal(input, &obj); err != nil {
			return nil, err
		}
		name := obj["metadata"].(map[string]any)["name"].(string)
		f.objects["horizontalpodautoscaler/"+name] = obj
	// Remove only owned scaling authority and retain evidence when deletion fails.
	case "delete":
		f.deletes++
		// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
		if f.failDelete {
			return nil, errors.New("injected deletion failure")
		}
		delete(f.objects, args[1]+"/"+args[2])
	}
	return nil, nil
}

// TestHPAInstallRecoveryAndPauseFence proves crash-retry ownership and durable pause before deletion.
func TestHPAInstallRecoveryAndPauseFence(t *testing.T) {
	f, s := newHPAFixture(t)
	f.failApply = true
	// Require the rejected or faulted operation to fail instead of silently granting authority.
	if err := f.runtime.reconcileAutoscaler(context.Background(), s, s.Apps["app"]); err == nil {
		t.Fatal("failed install accepted")
	}
	f.failApply = false
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if _, err := f.runtime.ReconcileHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, err := f.runtime.base.read()
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil || persisted.Apps["app"].Autoscaling.Phase != "active" {
		t.Fatal("pending installation not recovered", err)
	}
	f.failDelete = true
	// Require the rejected or faulted operation to fail instead of silently granting authority.
	if err = f.runtime.pauseAutoscaling(context.Background(), persisted, "app", "manual stop"); err == nil {
		t.Fatal("deletion failure accepted")
	}
	persisted, err = f.runtime.base.read()
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil || persisted.Apps["app"].Autoscaling.Phase != "paused" {
		t.Fatal("revocation not durable before API deletion", err)
	}
	f.failDelete = false
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if _, err = f.runtime.ReconcileHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
	if f.objects["horizontalpodautoscaler/autoscaler"] != nil {
		t.Fatal("restart retained revoked HPA")
	}
}

// TestHPATargetReplacementAndPolicyDriftCannotRenewDelegation verifies immutable ownership fences.
func TestHPATargetReplacementAndPolicyDriftCannotRenewDelegation(t *testing.T) {
	// Exercise each independent policy or recovery case with its own fixture state.
	for _, drift := range []string{"targetUID", "maximum", "deleted", "foreign"} {
		// Each subtest owns independent durable state and Kubernetes identities.
		t.Run(drift, func(t *testing.T) {
			f, s := newHPAFixture(t)
			// Require a successful fixture operation or authoritative observation before continuing safety assertions.
			if err := f.runtime.reconcileAutoscaler(context.Background(), s, s.Apps["app"]); err != nil {
				t.Fatal(err)
			}
			mutateHPAFixture(f, drift)
			_, _ = f.runtime.ReconcileHealth(context.Background())
			persisted, err := f.runtime.base.read()
			// Require a successful fixture operation or authoritative observation before continuing safety assertions.
			if err != nil || persisted.Apps["app"].Autoscaling.Phase != "paused" {
				t.Fatal("drift did not fence delegation", err)
			}
			// Preserve foreign resources while revoking only the delegation owned by this fixture.
			if drift == "foreign" && f.deletes != 0 {
				t.Fatal("foreign autoscaler deleted")
			}
			// Preserve foreign resources while revoking only the delegation owned by this fixture.
			if drift != "foreign" && f.objects["horizontalpodautoscaler/autoscaler"] != nil {
				t.Fatal("drifted HPA retained")
			}
		})
	}
}

// TestHPAMetricsOutageAndForeignReplicaWriter ensures manual drift and persistent outages revoke authority.
func TestHPAMetricsOutageAndForeignReplicaWriter(t *testing.T) {
	// Exercise each independent policy or recovery case with its own fixture state.
	for _, cause := range []string{"outage", "zero", "writer", "invalid-phase"} {
		// Each subtest owns independent durable state and Kubernetes identities.
		t.Run(cause, func(t *testing.T) {
			f, s := newHPAFixture(t)
			// Require a successful fixture operation or authoritative observation before continuing safety assertions.
			if err := f.runtime.reconcileAutoscaler(context.Background(), s, s.Apps["app"]); err != nil {
				t.Fatal(err)
			}
			app := s.Apps["app"]
			mutateHPACause(f, &app, cause)
			s.Apps["app"] = app
			// Require a successful fixture operation or authoritative observation before continuing safety assertions.
			if err := f.runtime.base.save(s); err != nil {
				t.Fatal(err)
			}
			// Require a successful fixture operation or authoritative observation before continuing safety assertions.
			if _, err := f.runtime.ReconcileHealth(context.Background()); err != nil {
				t.Fatal(err)
			}
			persisted, err := f.runtime.base.read()
			// Require a successful fixture operation or authoritative observation before continuing safety assertions.
			if err != nil || persisted.Apps["app"].Autoscaling.Phase != "paused" || f.objects["horizontalpodautoscaler/autoscaler"] != nil {
				t.Fatal("unsafe delegation survived", cause, err)
			}
		})
	}
}

// hpaFixtureCommand identifies the object commands after explicit kubectl placement flags.
func hpaFixtureCommand(command string) bool {
	return command == "get" || command == "apply" || command == "delete"
}

// mutateHPAFixture supplies one external ownership change for each recovery scenario.
func mutateHPAFixture(f *hpaFixture, drift string) {
	hpa := f.objects["horizontalpodautoscaler/autoscaler"]
	// Select the one policy or ownership fault exercised by this isolated scenario.
	switch drift {
	// Replace target identity without granting the new object automatic authority.
	case "targetUID":
		f.objects["deployment/version"]["metadata"].(map[string]any)["uid"] = "replaced"
	// Inject an HPA maximum beyond the reviewed capacity bound.
	case "maximum":
		hpa["spec"].(map[string]any)["maxReplicas"] = float64(20)
	// Remove the HPA externally to require fresh approval before reactivation.
	case "deleted":
		delete(f.objects, "horizontalpodautoscaler/autoscaler")
	// Keep the foreign object untouched while fencing the original delegation.
	case "foreign":
		hpa["metadata"].(map[string]any)["labels"].(map[string]any)["io.hostlier.owner"] = "foreign"
	}
}

// mutateHPACause injects one outage or manual writer without duplicating test assertions.
func mutateHPACause(f *hpaFixture, app *appRecord, cause string) {
	// Select the one policy or ownership fault exercised by this isolated scenario.
	switch cause {
	// Reject an unrecognized lifecycle journal phase rather than treating it as approval.
	case "invalid-phase":
		app.Autoscaling.Phase = "unrecognized"
	// Inject a prolonged observation outage without computing a custom CPU recommendation.
	case "outage":
		app.Autoscaling.MetricsUnavailableSince = time.Now().Add(-6 * time.Minute).Unix()
	// Exercise explicit stopped capacity without permitting automatic wake.
	case "zero":
		f.objects["deployment/version"]["spec"] = map[string]any{"replicas": float64(0)}
	// Inject a foreign manager owning the deployment replica field.
	case "writer":
		f.objects["deployment/version"]["metadata"].(map[string]any)["managedFields"] = []any{map[string]any{"manager": "kubectl-scale", "time": time.Now().UTC().Format(time.RFC3339), "fieldsV1": map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{}}}}}
	}
}

// TestHPAClusterStartupDoesNotRevokeDelegation preserves valid native ownership during API startup.
func TestHPAClusterStartupDoesNotRevokeDelegation(t *testing.T) {
	f, s := newHPAFixture(t)
	// Establish an installed bounded native controller before simulating the VM restart window.
	if err := f.runtime.reconcileAutoscaler(context.Background(), s, s.Apps["app"]); err != nil {
		t.Fatal(err)
	}
	f.failGet = true
	_, err := f.runtime.ReconcileHealth(context.Background())
	// An unavailable API remains an observation error, not a durable permission revocation.
	if err == nil {
		t.Fatal("unavailable cluster API reported success")
	}
	persisted, err := f.runtime.base.read()
	// The approved HPA must remain active and present while Kubernetes initializes.
	if err != nil || persisted.Apps["app"].Autoscaling.Phase != "active" || f.objects["horizontalpodautoscaler/autoscaler"] == nil {
		t.Fatal("startup revoked valid delegation", err)
	}
}

// get simulates authoritative object absence separately from cluster startup transport failure.
func (f *hpaFixture) get(args []string) ([]byte, error) {
	// A transient API failure must never be mistaken for authoritative target absence.
	if f.failGet {
		return nil, model.Err("KUBERNETES_COMMAND", "API starting")
	}
	object := f.objects[args[1]+"/"+args[2]]
	// Kubernetes ignore-not-found emits empty output only after a successful absence lookup.
	if object == nil {
		return nil, nil
	}
	return json.Marshal(object)
}
