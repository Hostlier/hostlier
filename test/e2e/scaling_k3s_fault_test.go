package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// k3sFaultFixture owns independent application namespaces and an isolated verified HTTPS edge.
type k3sFaultFixture struct {
	ctx       context.Context
	options   hostruntime.Options
	runtime   model.Runtime
	namespace string
	get       func(string) (int, string)
}

// newK3sFaultFixture creates only temporary owner-scoped resources without touching installed applications.
func newK3sFaultFixture(t *testing.T) k3sFaultFixture {
	t.Helper()
	// Cluster mutations require explicit disposable-local-VM opt in.
	if os.Getenv("HOSTLIER_K3S_SCALING_FAULTS") != "1" {
		t.Skip("set HOSTLIER_K3S_SCALING_FAULTS=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	sum := sha256.Sum256([]byte(dir))
	owner := hex.EncodeToString(sum[:])[:16]
	admin, port, pool := startCaddyFixture(t, os.Getenv("HOSTLIER_CADDY_BINARY"), dir)
	options := hostruntime.Options{Type: "k3s", StateDir: dir, CaddyAdmin: admin, SkipPublicHTTPSProbe: true, HealthTimeout: 30 * time.Second}
	adapter, err := hostruntime.New(options)
	// Native runtime configuration must succeed before any cluster writes.
	if err != nil {
		t.Fatal(err)
	}
	cleanupAgentBackend(t, "k3s", owner)
	return k3sFaultFixture{ctx: ctx, options: options, runtime: adapter, namespace: "hostlier-" + owner + "-fault", get: agentHTTPSGetter(fixtureHTTPSGetter(t, port, pool))}
}

// kubeFault executes bounded real kubectl commands and returns their protected fixture output.
func kubeFault(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/local/bin/k3s", append([]string{"kubectl"}, args...)...)
	output, err := command.CombinedOutput()
	// Failed observations cannot establish successful rollback or ownership isolation.
	if err != nil {
		t.Fatalf("kubectl %v: %v %s", args, err, output)
	}
	return string(output)
}

// faultProject resolves the installed fixture's immutable image through read-only cluster inspection.
func faultProject(t *testing.T, stateful bool) model.Project {
	t.Helper()
	raw := kubeFault(t, "get", "deploy", "-A", "-l", "io.hostlier.app=fixture", "-o", "jsonpath={.items[0].spec.template.spec.containers[0].image}")
	// Existing fixture metadata supplies a cached immutable nginx image without modifying that fixture.
	if !strings.Contains(raw, "@sha256:") {
		t.Fatal("immutable installed fixture image unavailable", raw)
	}
	replicas := 2
	spec := model.Project{Version: 1, Name: "fault", Image: raw, Port: 80, Route: "fault.hostlier.test", Replicas: &replicas, Resources: &model.Limits{CPU: "0.1", Memory: "64Mi"}}
	// Persistent local-path writers intentionally run as a singleton.
	if stateful {
		one := 1
		spec.Replicas = &one
		spec.Volumes = map[string]model.Volume{"data": {Mount: "/usr/share/nginx/html/data", Size: "1Gi"}}
	}
	return spec
}

// TestK3sScalingCapacityAndRollback proves capacity denial and historical allocation restoration.
func TestK3sScalingCapacityAndRollback(t *testing.T) {
	// Exercise overlapping stateless capacity separately from an exclusive PVC writer.
	for _, stateful := range []bool{false, true} {
		name := "stateless"
		// Name the PVC case explicitly in durable test evidence.
		if stateful {
			name = "volume"
		}
		// Each case has an independent runtime owner and namespace.
		t.Run(name, func(t *testing.T) { checkK3sCapacityRollback(t, stateful) })
	}
}

// checkK3sCapacityRollback retains serving source data during a real unschedulable candidate.
func checkK3sCapacityRollback(t *testing.T, stateful bool) {
	f := newK3sFaultFixture(t)
	spec := faultProject(t, stateful)
	first := scalingDeploy(t, f.ctx, f.runtime, spec, "scaled-revision")
	writeK3sFaultData(t, f)
	scalingData(t, f.get, k3sDataURL(first.URL, stateful))
	denied := spec
	denied.Resources = &model.Limits{CPU: "1024", Memory: "64Mi"}
	failed := make(chan error, 1)
	// Run scheduling concurrently so source traffic and pending-pod evidence can be observed.
	go func() {
		_, err := f.runtime.Deploy(f.ctx, denied, model.DeployContext{OperationID: "capacity-denied"})
		failed <- err
	}()
	observeK3sCapacityFailure(t, f, first.URL, stateful, failed)
	scalingData(t, f.get, k3sDataURL(first.URL, stateful))
	assertK3sFaultLimits(t, f, model.DesiredReplicas(spec), "100m", "64Mi")
	next := spec
	one := 1
	next.Replicas = &one
	next.Resources = &model.Limits{CPU: "0.2", Memory: "96Mi"}
	scalingDeploy(t, f.ctx, f.runtime, next, "second-scaled-revision")
	assertK3sFaultLimits(t, f, 1, "200m", "96Mi")
	scalingDeploy(t, f.ctx, f.runtime, spec, "rollback-to-scaled-revision")
	assertK3sFaultLimits(t, f, model.DesiredReplicas(spec), "100m", "64Mi")
	// The same PVC must retain writes across failure and both historical revisions.
	if stateful {
		scalingData(t, f.get, k3sDataURL(first.URL, stateful))
	}
}

// writeK3sFaultData writes an independently verifiable response into every source pod.
func writeK3sFaultData(t *testing.T, f k3sFaultFixture) {
	t.Helper()
	pods := strings.Fields(kubeFault(t, "get", "pods", "-n", f.namespace, "-o", "jsonpath={.items[*].metadata.name}"))
	// Every healthy source replica receives the same durable-response marker.
	for _, pod := range pods {
		kubeFault(t, "exec", "-n", f.namespace, pod, "--", "sh", "-c", "echo scaling-data > /usr/share/nginx/html/index.html; if test -d /usr/share/nginx/html/data; then echo scaling-data > /usr/share/nginx/html/data/index.html; fi")
	}
}

// observeK3sCapacityFailure requires actual scheduler denial and keeps stateless source HTTPS live.
func observeK3sCapacityFailure(t *testing.T, f k3sFaultFixture, address string, stateful bool, done <-chan error) {
	t.Helper()
	unschedulable := false
	requests := 0
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	// Observe until the bounded native rollout rejects unschedulable requested capacity.
	for {
		// Wait for scheduling evidence, operation completion or the fixture deadline.
		select {
		// Rollout failure must leave evidence of genuine scheduler capacity denial.
		case err := <-done:
			assertCapacityDenied(t, err, unschedulable, stateful, requests)
			return
		// Kubernetes conditions distinguish unschedulability from an unrelated readiness failure.
		case <-ticker.C:
			conditions := kubeFault(t, "get", "pods", "-n", f.namespace, "-o", "jsonpath={.items[*].status.conditions[*].message}")
			unschedulable = unschedulable || strings.Contains(conditions, "Insufficient cpu")
			// Stateless source capacity must continue serving until a replacement is ready.
			if !stateful {
				scalingData(t, f.get, address)
				requests++
			}
		// Bound the observer independently from any failed command.
		case <-f.ctx.Done():
			t.Fatal("capacity test deadline exceeded")
		}
	}
}

// assertK3sFaultLimits inspects actual ready Deployment capacity and exact resource enforcement.
func assertK3sFaultLimits(t *testing.T, f k3sFaultFixture, count int, cpu, memory string) {
	t.Helper()
	raw := kubeFault(t, "get", "deploy", "-n", f.namespace, "-o", "json")
	var list struct {
		Items []struct {
			Spec struct {
				Replicas int
				Template struct {
					Spec struct {
						Containers []struct {
							Resources struct{ Limits map[string]string }
						}
					}
				}
			}
			Status struct{ ReadyReplicas int }
		}
	}
	// Runtime observations must decode independently of retained Hostlier desired state.
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	desired, ready := 0, 0
	// Superseded revisions may exist stopped; only active capacity may consume resources.
	for _, item := range list.Items {
		desired += item.Spec.Replicas
		ready += item.Status.ReadyReplicas
		// The approved historical allocation must be present on every active version.
		if item.Spec.Replicas > 0 {
			limits := item.Spec.Template.Spec.Containers[0].Resources.Limits
			// Both enforcement dimensions must match the historical approved revision.
			if limits["cpu"] != cpu || limits["memory"] != memory {
				t.Fatal("allocation differs", limits, cpu, memory)
			}
		}
	}
	// Ready and desired capacity must both match the exact approved revision.
	if desired != count || ready != count {
		t.Fatalf("capacity desired=%d ready=%d expected=%d", desired, ready, count)
	}
}

// TestK3sScalingCrashChild hard-exits only in an explicitly spawned isolated worker.
func TestK3sScalingCrashChild(t *testing.T) {
	path := os.Getenv("HOSTLIER_K3S_CRASH_REQUEST")
	// Ordinary test discovery must never terminate the test runner.
	if path == "" {
		t.Skip("isolated crash worker only")
	}
	raw, err := os.ReadFile(path)
	// The child must receive its exact parent's protected fixture settings.
	if err != nil {
		t.Fatal(err)
	}
	var request crashRequest
	// Decode public runtime settings without copying any production secrets.
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	options := hostruntime.Options{Type: "k3s", StateDir: request.State, CaddyAdmin: request.Admin, SkipPublicHTTPSProbe: true, HealthTimeout: 20 * time.Second}
	// Forward actual cluster writes before deliberately bypassing deferred rollback.
	options.KubeRun = func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		command := exec.CommandContext(ctx, "/usr/local/bin/k3s", args...)
		command.Stdin = strings.NewReader(string(input))
		output, err := command.Output()
		var manifest struct{ Kind string }
		_ = json.Unmarshal(input, &manifest)
		target := map[string]string{"candidate": "Deployment", "service": "Service"}[request.Stage]
		// The selected real successful API mutation establishes the hard-crash boundary.
		if err == nil && manifest.Kind == target {
			os.Exit(91)
		}
		return output, err
	}
	adapter, err := hostruntime.New(options)
	// Configuration errors must fail before any deliberate termination.
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Deploy(context.Background(), request.Spec, model.DeployContext{OperationID: "interrupted-" + request.Stage})
	t.Fatalf("hard crash boundary was not reached: %v", err)
}

// TestK3sScalingProcessCrash checks v1 journals before candidate readiness and after Service cutover.
func TestK3sScalingProcessCrash(t *testing.T) {
	// Independent owner scopes cover overlapping capacity and exclusive persistent writers.
	for _, stateful := range []bool{false, true} {
		label := "stateless"
		// PVC cases must prove writes survive abrupt writer handoff.
		if stateful {
			label = "volume"
		}
		// Both boundaries bypass all deferred cleanup in a separate process.
		for _, stage := range []string{"candidate", "service"} {
			// Keep recovery evidence and ownership isolated per deliberate process crash.
			t.Run(label+"/"+stage, func(t *testing.T) { checkK3sProcessCrash(t, stateful, stage) })
		}
	}
}

// checkK3sProcessCrash starts a fresh runtime after a genuine hard-exit cluster mutation.
func checkK3sProcessCrash(t *testing.T, stateful bool, stage string) {
	f := newK3sFaultFixture(t)
	spec := faultProject(t, stateful)
	first := scalingDeploy(t, f.ctx, f.runtime, spec, "original")
	writeK3sFaultData(t, f)
	next := spec
	next.Resources = &model.Limits{CPU: "0.2", Memory: "96Mi"}
	request := crashRequest{State: f.options.StateDir, Admin: f.options.CaddyAdmin, Stage: stage, Spec: next}
	runK3sCrashProcess(t, f.ctx, request)
	restarted, err := hostruntime.New(f.options)
	// Fresh process-equivalent state must recover from durable intent rather than memory flags.
	if err != nil {
		t.Fatal(err)
	}
	recovered := scalingDeploy(t, f.ctx, restarted, next, "recovered")
	assertK3sFaultLimits(t, f, model.DesiredReplicas(next), "200m", "96Mi")
	// The interrupted candidate must be removed rather than leaked as an extra stopped revision.
	sum := sha256.Sum256([]byte("interrupted-" + stage))
	candidate := "app-" + hex.EncodeToString(sum[:])[:16]
	result := kubeFault(t, "get", "deploy", candidate, "-n", f.namespace, "--ignore-not-found", "-o", "name")
	// Recovery may not leave the journal-owned uncommitted candidate behind.
	if strings.TrimSpace(result) != "" {
		t.Fatal("interrupted candidate leaked", result)
	}
	// Persistent content survives two writer handoffs and a process exit without deferred rollback.
	if stateful {
		scalingData(t, f.get, k3sDataURL(recovered.URL, stateful))
	}
	status, _ := f.get(first.URL)
	// Recovered traffic must reach verified healthy capacity on the original route reservation.
	if status != 200 {
		t.Fatal("recovered route unavailable", status)
	}
}

// runK3sCrashProcess requires the exact deliberate hard-exit result from the isolated worker.
func runK3sCrashProcess(t *testing.T, ctx context.Context, request crashRequest) {
	t.Helper()
	raw, err := json.Marshal(request)
	// Serialization failure cannot establish a crash recovery gate.
	if err != nil {
		t.Fatal(err)
	}
	path := request.State + "/k3s-crash-request.json"
	// Keep crash settings protected like ordinary runtime metadata.
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestK3sScalingCrashChild$")
	child.Env = append(os.Environ(), "HOSTLIER_K3S_CRASH_REQUEST="+path)
	output, err := child.CombinedOutput()
	failure, ok := err.(*exec.ExitError)
	// Exit 91 proves rollback defers were bypassed at the exact external mutation boundary.
	if !ok || failure.ExitCode() != 91 {
		t.Fatalf("crash worker: %v %s", err, output)
	}
}

// assertCapacityDenied requires scheduler evidence and observed traffic before accepting a failed rollout.
func assertCapacityDenied(t *testing.T, err error, unschedulable, stateful bool, requests int) {
	t.Helper()
	// A generic timeout is insufficient evidence without an actual scheduler denial.
	if err == nil || !unschedulable {
		t.Fatal("capacity denial not established", err, unschedulable)
	}
	var failure *model.Error
	// Scheduler diagnostics must survive the runtime transaction boundary as a typed failure.
	if !errors.As(err, &failure) || failure.Code != "UNSCHEDULABLE" {
		t.Fatalf("expected typed UNSCHEDULABLE, got %v", err)
	}
	t.Logf("Observed actual Insufficient cpu scheduler denial; preserved source HTTPS reads: %d", requests)
	// Stateless sources must have served verified traffic while the candidate was pending.
	if !stateful && requests == 0 {
		t.Fatal("source traffic was not observed during capacity denial")
	}
}

// k3sDataURL reads persisted content through nginx without masking its initial image readiness page.
func k3sDataURL(address string, stateful bool) string {
	// Mounted subdirectories start empty, while nginx's image-provided root stays ready.
	if stateful {
		return address + "/data/"
	}
	return address
}
