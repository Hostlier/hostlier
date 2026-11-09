package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
)

// TestNativeHPACPU proves real metrics-server CPU scaling and stable certificate-valid HTTPS.
func TestNativeHPACPU(t *testing.T) {
	// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
	if os.Getenv("HOSTLIER_AUTOSCALING_E2E") != "1" {
		t.Skip("set HOSTLIER_AUTOSCALING_E2E=1 in the disposable k3s VM")
	}
	fixture := newK3sFaultFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	spec := faultProject(t, false)
	one := 1
	spec.Replicas = &one
	spec.Requests = &model.Limits{CPU: "0.01", Memory: "32Mi"}
	spec.Resources = &model.Limits{CPU: "0.1", Memory: "64Mi"}
	spec.Autoscaling = &model.Autoscaling{MinReplicas: 1, MaxReplicas: 3, CPUTargetPercent: 65}
	deployed, err := fixture.runtime.Deploy(ctx, spec, model.DeployContext{OperationID: "native-cpu-hpa"})
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil {
		t.Fatal(err)
	}
	namespace := fixture.namespace
	pod := kubeFault(t, "get", "pods", "-n", namespace, "-l", "io.hostlier.version="+deployed.RuntimeID, "-o", "jsonpath={.items[0].metadata.name}")
	// The load runs in the application container, so the genuine resource metrics API measures it.
	kubeFault(t, "exec", "-n", namespace, pod, "--", "sh", "-c", "sh -c 'while :; do :; done' </dev/null >/dev/null 2>&1 & echo $! >/tmp/hostlier-cpu-burn.pid")
	// Cleanup stops only this fixture process and leaves installed workloads unchanged.
	t.Cleanup(func() {
		exec.Command("/usr/local/bin/k3s", "kubectl", "exec", "-n", namespace, pod, "--", "sh", "-c", "kill $(cat /tmp/hostlier-cpu-burn.pid)").Run()
	})
	waitHPACount(t, ctx, fixture, deployed.RuntimeID, 2, true)
	waitHPACount(t, ctx, fixture, deployed.RuntimeID, 3, true)
	// Low background nginx CPU must eventually downscale after the native stabilization/rate windows.
	kubeFault(t, "exec", "-n", namespace, pod, "--", "sh", "-c", "kill $(cat /tmp/hostlier-cpu-burn.pid)")
	waitHPACount(t, ctx, fixture, deployed.RuntimeID, 1, false)
	// A manual stop must durably remove HPA before replicas reach zero.
	if err = fixture.runtime.Action(ctx, spec.Name, "stop", model.ActionContext{}); err != nil {
		t.Fatal(err)
	}
	// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
	if raw := kubeFault(t, "get", "hpa", "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}"); raw != "" {
		t.Fatal("manual stop retained HPA", raw)
	}
	// Restarting the lifecycle reconciler cannot wake explicitly stopped capacity.
	monitor := fixture.runtime.(interface {
		ReconcileHealth(context.Context) (map[string]string, error)
	})
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if _, err = monitor.ReconcileHealth(ctx); err != nil {
		t.Fatal(err)
	}
	// Confirm stopped or retired capacity remains exactly zero in the actual cluster.
	if raw := kubeFault(t, "get", "deploy", deployed.RuntimeID, "-n", namespace, "-o", "jsonpath={.spec.replicas}"); raw != "0" {
		t.Fatal("stopped workload woke", raw)
	}
}

// waitHPACount observes actual ready pods, native HPA conditions and trusted application TLS.
func waitHPACount(t *testing.T, ctx context.Context, fixture k3sFaultFixture, version string, target int, atLeast bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	// Poll native state within the fixture deadline while retaining independent serving assertions.
	for {
		raw := kubeFault(t, "get", "deploy", version, "-n", fixture.namespace, "-o", "json")
		var object struct {
			Spec   struct{ Replicas int }
			Status struct{ ReadyReplicas int }
		}
		// Require a successful fixture operation or authoritative observation before continuing safety assertions.
		if err := json.Unmarshal([]byte(raw), &object); err != nil {
			t.Fatal(err)
		}
		// Keep actual desired and ready capacity within the reviewed replica contract.
		if object.Spec.Replicas > 3 {
			t.Fatal("HPA exceeded approved maximum", object.Spec.Replicas)
		}
		code, _ := fixture.get("https://fault.hostlier.test")
		// Keep certificate-validated application HTTPS serving through the native lifecycle transition.
		if code != 200 {
			t.Fatal("HTTPS unavailable during native scaling", code)
		}
		t.Logf("%s desired=%d ready=%d target=%d", time.Now().UTC().Format(time.RFC3339), object.Spec.Replicas, object.Status.ReadyReplicas, target)
		reached := hpaCountReached(object.Spec.Replicas, object.Status.ReadyReplicas, target, atLeast)
		// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
		if reached {
			return
		}
		// Respect cancellation while waiting for native state to converge.
		select {
		// Shutdown or the bounded observation timer controls the next fixture step.
		case <-ctx.Done():
			t.Fatal("native HPA did not reach " + strconv.Itoa(target))
		// Shutdown or the bounded observation timer controls the next fixture step.
		case <-ticker.C:
		}
	}
}

// TestNativeHPARevisionHandoff verifies real target transfer, replica drift revocation and zero after adapter restart.
func TestNativeHPARevisionHandoff(t *testing.T) {
	// Mutations are confined to this disposable VM and the generated fixture owner.
	requireNativeHPAE2E(t)
	f := newK3sFaultFixture(t)
	spec := faultProject(t, false)
	one := 1
	spec.Replicas = &one
	spec.Requests = &model.Limits{CPU: "0.01", Memory: "32Mi"}
	spec.Autoscaling = &model.Autoscaling{MinReplicas: 1, MaxReplicas: 3, CPUTargetPercent: 65}
	first := deployHPAFixture(t, f, spec, "handoff-first")
	var err error
	monitor := f.runtime.(interface {
		ReconcileHealth(context.Context) (map[string]string, error)
	})
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if _, err = monitor.ReconcileHealth(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
	if count := kubeFault(t, "get", "hpa", "-n", f.namespace, "-o", "jsonpath={.items[*].spec.scaleTargetRef.name}"); count != first.RuntimeID {
		t.Fatal("reconciliation revoked valid target", count)
	}
	second := deployHPAFixture(t, f, spec, "handoff-second")
	// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
	if target := kubeFault(t, "get", "hpa", "-n", f.namespace, "-o", "jsonpath={.items[*].spec.scaleTargetRef.name}"); target != second.RuntimeID {
		t.Fatal("HPA did not transfer", target)
	}
	// Confirm stopped or retired capacity remains exactly zero in the actual cluster.
	if count := kubeFault(t, "get", "deploy", first.RuntimeID, "-n", f.namespace, "-o", "jsonpath={.spec.replicas}"); count != "0" {
		t.Fatal("source retained capacity", count)
	}
	// An explicit foreign replica writer revokes delegation without fighting its chosen capacity.
	kubeFault(t, "patch", "deploy", second.RuntimeID, "-n", f.namespace, "--type=merge", "--field-manager=hostlier-e2e-external", "-p", `{"spec":{"replicas":2}}`)
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if _, err = monitor.ReconcileHealth(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Revocation must leave no native HPA capable of changing stopped or manual capacity.
	if hpa := kubeFault(t, "get", "hpa", "-n", f.namespace, "-o", "jsonpath={.items[*].metadata.name}"); hpa != "" {
		t.Fatal("foreign replica writer did not pause HPA", hpa)
	}
	// Only a newly approved replacement may resume a paused policy.
	_ = deployHPAFixture(t, f, spec, "handoff-resume")
	spec.Autoscaling = nil
	zero := 0
	spec.Replicas = &zero
	stopped := deployHPAFixture(t, f, spec, "handoff-zero")
	restarted := restartHPAFixture(t, f.options)
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if _, err = restarted.(interface {
		ReconcileHealth(context.Context) (map[string]string, error)
	}).ReconcileHealth(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Confirm stopped or retired capacity remains exactly zero in the actual cluster.
	if count := kubeFault(t, "get", "deploy", stopped.RuntimeID, "-n", f.namespace, "-o", "jsonpath={.spec.replicas}"); count != "0" {
		t.Fatal("zero woke after runtime restart", count)
	}
	// Require the rejected or faulted operation to fail instead of silently granting authority.
	if err = restarted.Action(f.ctx, spec.Name, "start", model.ActionContext{}); err == nil {
		t.Fatal("lifecycle start bypassed zero approval")
	}
}

// TestNativeHPAActiveReconcile verifies native CPU ownership remains active under controller observations.
func TestNativeHPAActiveReconcile(t *testing.T) {
	// The fixture owns a fresh namespace and never changes installed workloads.
	requireNativeHPAE2E(t)
	f := newK3sFaultFixture(t)
	spec := faultProject(t, false)
	one := 1
	spec.Replicas = &one
	spec.Requests = &model.Limits{CPU: "0.01", Memory: "32Mi"}
	spec.Autoscaling = &model.Autoscaling{MinReplicas: 1, MaxReplicas: 2, CPUTargetPercent: 65}
	deployed := deployHPAFixture(t, f, spec, "active-cpu-ownership")
	var err error
	pod := kubeFault(t, "get", "pods", "-n", f.namespace, "-l", "io.hostlier.version="+deployed.RuntimeID, "-o", "jsonpath={.items[0].metadata.name}")
	kubeFault(t, "exec", "-n", f.namespace, pod, "--", "sh", "-c", "sh -c 'while :; do :; done' </dev/null >/dev/null 2>&1 & echo $! >/tmp/hostlier-cpu-burn.pid")
	monitor := f.runtime.(interface {
		ReconcileHealth(context.Context) (map[string]string, error)
	})
	deadline := time.Now().Add(3 * time.Minute)
	// Reconcile during the native scale rather than observing only its completed result.
	for time.Now().Before(deadline) {
		// Require a successful fixture operation or authoritative observation before continuing safety assertions.
		if _, err = monitor.ReconcileHealth(f.ctx); err != nil {
			t.Fatal(err)
		}
		// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
		if hpa := kubeFault(t, "get", "hpa", "-n", f.namespace, "-o", "jsonpath={.items[*].metadata.name}"); hpa != "autoscaler" {
			t.Fatal("native replica writer was falsely revoked")
		}
		code, _ := f.get("https://fault.hostlier.test")
		// Keep certificate-validated application HTTPS serving through the native lifecycle transition.
		if code != 200 {
			t.Fatal("TLS routing failed during native ownership", code)
		}
		// Keep actual desired and ready capacity within the reviewed replica contract.
		if count := kubeFault(t, "get", "deploy", deployed.RuntimeID, "-n", f.namespace, "-o", "jsonpath={.status.readyReplicas}"); count == "2" {
			// Native HPA manager fields now reflect a real CPU-driven scale; verify them once more.
			if _, err = monitor.ReconcileHealth(f.ctx); err != nil {
				t.Fatal(err)
			}
			// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
			if hpa := kubeFault(t, "get", "hpa", "-n", f.namespace, "-o", "jsonpath={.items[*].metadata.name}"); hpa != "autoscaler" {
				t.Fatal("native scale ownership paused")
			}
			// Require a successful fixture operation or authoritative observation before continuing safety assertions.
			if err = f.runtime.Action(f.ctx, spec.Name, "stop", model.ActionContext{}); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatal("native HPA did not reach two ready replicas under continuous controller observation")
}

// TestNativeHPACapacityKeepsServing proves a capped pending replica cannot trigger node provisioning.
func TestNativeHPACapacityKeepsServing(t *testing.T) {
	// Capacity reservation and workload both belong to the generated fixture namespace.
	requireNativeHPAE2E(t)
	f := newK3sFaultFixture(t)
	spec := faultProject(t, false)
	one := 1
	spec.Replicas = &one
	spec.Resources = &model.Limits{CPU: "0.3", Memory: "64Mi"}
	spec.Requests = &model.Limits{CPU: "0.3", Memory: "32Mi"}
	spec.Autoscaling = &model.Autoscaling{MinReplicas: 1, MaxReplicas: 2, CPUTargetPercent: 65}
	deployed := deployHPAFixture(t, f, spec, "pending-native-hpa")
	var err error
	reservation := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "cpu-reservation", "namespace": f.namespace}, "spec": map[string]any{"containers": []any{map[string]any{"name": "reserve", "image": spec.Image, "resources": map[string]any{"requests": map[string]string{"cpu": "0.9", "memory": "16Mi"}, "limits": map[string]string{"cpu": "0.9", "memory": "32Mi"}}}}}}
	raw, _ := json.Marshal(reservation)
	path := t.TempDir() + "/reservation.json"
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	kubeFault(t, "apply", "-f", path)
	kubeFault(t, "wait", "pod/cpu-reservation", "-n", f.namespace, "--for=condition=Ready", "--timeout=60s")
	pod := kubeFault(t, "get", "pods", "-n", f.namespace, "-l", "io.hostlier.version="+deployed.RuntimeID, "-o", "jsonpath={.items[0].metadata.name}")
	kubeFault(t, "exec", "-n", f.namespace, pod, "--", "sh", "-c", "sh -c 'while :; do :; done' </dev/null >/dev/null 2>&1 & echo $! >/tmp/hostlier-cpu-burn.pid")
	deadline := time.Now().Add(3 * time.Minute)
	pending := false
	// Observe real scheduling failures while asserting the original HTTPS backend remains ready.
	for time.Now().Before(deadline) {
		pods := kubeFault(t, "get", "pods", "-n", f.namespace, "-l", "io.hostlier.version="+deployed.RuntimeID, "-o", "json")
		code, _ := f.get("https://fault.hostlier.test")
		// Keep certificate-validated application HTTPS serving through the native lifecycle transition.
		if code != 200 {
			t.Fatal("capacity exhaustion withdrew healthy HTTPS", code)
		}
		// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
		if strings.Contains(pods, `"reason": "Unschedulable"`) {
			pending = true
			break
		}
		time.Sleep(3 * time.Second)
	}
	// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
	if !pending {
		t.Fatal("extra native HPA replica did not become unschedulable")
	}
	// Keep actual desired and ready capacity within the reviewed replica contract.
	if count := kubeFault(t, "get", "deploy", deployed.RuntimeID, "-n", f.namespace, "-o", "jsonpath={.spec.replicas}"); count != "2" {
		t.Fatal("pending capacity escaped bound", count)
	}
	// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
	if nodes := kubeFault(t, "get", "nodes", "-o", "jsonpath={.items[*].metadata.name}"); nodes != "lima-hostlier-k3s" {
		t.Fatal("node inventory changed", nodes)
	}
	// Releasing only the test reservation permits the existing pending pod to converge normally.
	kubeFault(t, "delete", "pod/cpu-reservation", "-n", f.namespace, "--wait=true")
	waitHPACount(t, f.ctx, f, deployed.RuntimeID, 2, true)
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err = f.runtime.Action(f.ctx, spec.Name, "stop", model.ActionContext{}); err != nil {
		t.Fatal(err)
	}
}

// hpaCountReached compares actual ready capacity with an exact or lower-bound expectation.
func hpaCountReached(desired, ready, target int, atLeast bool) bool {
	// Intermediate upscale observations may already reach a later approved count.
	if atLeast {
		return desired >= target && ready >= target
	}
	return desired == target && ready == target
}

// deployHPAFixture applies one explicitly approved test revision and preserves its failure context.
func deployHPAFixture(t *testing.T, f k3sFaultFixture, spec model.Project, operation string) model.Deployment {
	t.Helper()
	deployed, err := f.runtime.Deploy(f.ctx, spec, model.DeployContext{OperationID: operation})
	// A failed deployment cannot serve as evidence of successful HPA handoff.
	if err != nil {
		t.Fatal(err)
	}
	return deployed
}

// restartHPAFixture reconstructs only the adapter while retaining its protected journal and owner.
func restartHPAFixture(t *testing.T, options hostruntime.Options) model.Runtime {
	t.Helper()
	adapter, err := hostruntime.New(options)
	// Missing state prevents any restart assertion from proceeding.
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

// requireNativeHPAE2E gates mutations to the explicitly selected disposable k3s VM.
func requireNativeHPAE2E(t *testing.T) {
	t.Helper()
	// Ordinary test runs never create or scale cluster resources.
	if os.Getenv("HOSTLIER_AUTOSCALING_E2E") != "1" {
		t.Skip("set HOSTLIER_AUTOSCALING_E2E=1")
	}
}
