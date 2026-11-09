package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// telemetryRuntimeFixture retains a primary, generic sidecar, previous runtime, and network holder.
func telemetryRuntimeFixture(t *testing.T) *Runtime {
	t.Helper()
	r := &Runtime{owner: "fixture-owner", options: Options{StateDir: t.TempDir()}}
	app := appRecord{Deployment: model.Deployment{Name: "demo", RuntimeID: strings.Repeat("a", 64)}, Anchor: strings.Repeat("d", 64), PreviousRuntimeID: strings.Repeat("c", 64), Members: map[string]string{"worker": strings.Repeat("b", 64), "orphan": strings.Repeat("e", 64)}, Spec: model.Project{Sidecars: map[string]model.Project{"worker": {Workload: "worker"}}}}
	// Persist through the production state writer so lookup paths cannot rely on test-only maps.
	if err := r.save(state{Apps: map[string]appRecord{"demo": app}}); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestDockerTelemetryResolvesOnlyCurrentOwnedMembers covers stable component and instance attribution.
func TestDockerTelemetryResolvesOnlyCurrentOwnedMembers(t *testing.T) {
	r := telemetryRuntimeFixture(t)
	r.docker = &http.Client{Transport: sidecarTransport(func(request *http.Request) (*http.Response, error) { // Return only the requested owned daemon object.
		id := strings.Split(request.URL.Path, "/")[2]
		return sidecarResponse(map[string]any{"Id": id, "Config": map[string]any{"Labels": r.labels("demo")}})
	})}
	// Both primary and generic worker sidecars require daemon ownership verification.
	for _, component := range []string{"app", "worker"} {
		id := strings.Repeat("a", 64)
		if component == "worker" { // The sidecar has a separate full daemon ID.
			id = strings.Repeat("b", 64)
		}
		identity, err := r.ResolveTelemetry(context.Background(), map[string]string{"container.id": id, "hostlier.app": "forged", "hostlier.component": "forged"})
		// User-supplied semantic labels cannot override the runtime's trusted mapping.
		if err != nil || identity.Application != "demo" || identity.Component != component || identity.Instance != id {
			t.Fatalf("%s: %+v %v", component, identity, err)
		}
	}
}

// TestDockerTelemetryRejectsUntrustedCoordinates excludes stale, infrastructure, and malformed records.
func TestDockerTelemetryRejectsUntrustedCoordinates(t *testing.T) {
	r := telemetryRuntimeFixture(t)
	// None of these identities is admitted far enough to query the daemon.
	for _, id := range []string{"", "application-name", strings.Repeat("a", 12), strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64)} {
		_, err := r.ResolveTelemetry(context.Background(), map[string]string{"container.id": id})
		if err == nil { // An ID hint alone cannot select another application or revision.
			t.Fatalf("accepted %q", id)
		}
	}
}

// TestDockerTelemetryRechecksDaemonOwnership rejects tampered labels and mismatched inspection IDs.
func TestDockerTelemetryRechecksDaemonOwnership(t *testing.T) {
	r := telemetryRuntimeFixture(t)
	// Full ownership and exact daemon identity are independent checks.
	for _, invalid := range []string{"owner", "application", "managed", "id"} {
		id, labels := strings.Repeat("a", 64), r.labels("demo")
		if invalid == "id" { // A daemon response cannot silently resolve a different object.
			id = strings.Repeat("f", 64)
		} else { // A valid container ID with foreign metadata is still inadmissible.
			keys := map[string]string{"owner": "io.hostlier.owner", "application": "io.hostlier.app", "managed": "io.hostlier.managed"}
			labels[keys[invalid]] = "foreign"
		}
		r.docker = &http.Client{Transport: sidecarTransport(func(*http.Request) (*http.Response, error) { // Inject exactly one ownership or identity mismatch.
			return sidecarResponse(map[string]any{"Id": id, "Config": map[string]any{"Labels": labels}})
		})}
		_, err := r.ResolveTelemetry(context.Background(), map[string]string{"container.id": strings.Repeat("a", 64)})
		if err == nil { // Daemon ownership failure must never return a trusted mapping.
			t.Fatal("accepted invalid", invalid)
		}
	}
}

// TestDockerTelemetryRechecksStateAfterInspection rejects a rollout concurrent with daemon I/O.
func TestDockerTelemetryRechecksStateAfterInspection(t *testing.T) {
	r := telemetryRuntimeFixture(t)
	r.docker = &http.Client{Transport: sidecarTransport(func(*http.Request) (*http.Response, error) { // Simulate a rollout while inspection is in flight.
		advanceTelemetryRevision(t, r)
		return sidecarResponse(map[string]any{"Id": strings.Repeat("b", 64), "Config": map[string]any{"Labels": r.labels("demo")}})
	})}
	_, err := r.ResolveTelemetry(context.Background(), map[string]string{"container.id": strings.Repeat("b", 64)})
	// Even a surviving member cannot carry the retired group's revision attribution.
	if err == nil {
		t.Fatal("admitted stale revision after concurrent rollout")
	}
}

// advanceTelemetryRevision also proves the mutation mutex is not held during network callbacks.
func advanceTelemetryRevision(t *testing.T, r *Runtime) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.read()
	// A failed fixture state read must not silently weaken the concurrency regression.
	if err != nil {
		t.Fatal(err)
	}
	app := s.Apps["demo"]
	app.RuntimeID = strings.Repeat("f", 64)
	s.Apps["demo"] = app
	// Persist the completed deployment while the telemetry request awaits its response.
	if err = r.save(s); err != nil {
		t.Fatal(err)
	}
}

// kubeTelemetryFixture provides real serialized Kubernetes object shapes through the command boundary.
func kubeTelemetryFixture(t *testing.T) (*K3s, map[string]string, map[string]any, map[string]any) {
	t.Helper()
	k := &K3s{base: telemetryRuntimeFixture(t)}
	attrs := map[string]string{"k8s.namespace.name": k.namespace("demo"), "k8s.pod.name": "app-demo-pod", "k8s.pod.uid": "fixture-pod-uid", "k8s.container.name": "worker"}
	ns := map[string]any{"metadata": map[string]any{"name": attrs["k8s.namespace.name"], "labels": kubeTelemetryLabels(k, false)}}
	pod := map[string]any{"metadata": map[string]any{"namespace": attrs["k8s.namespace.name"], "name": attrs["k8s.pod.name"], "uid": attrs["k8s.pod.uid"], "labels": kubeTelemetryLabels(k, true)}, "spec": map[string]any{"containers": []any{map[string]any{"name": "app"}}, "initContainers": []any{map[string]any{"name": "worker", "restartPolicy": "Always"}}}}
	k.options.KubeRun = func(_ context.Context, args []string, _ []byte) ([]byte, error) { // Return serialized cluster observations at the production command boundary.
		if args[5] == "namespace" { // The namespace is independently read before trusting a Pod.
			return json.Marshal(ns)
		}
		return json.Marshal(pod)
	}
	return k, attrs, ns, pod
}

// kubeTelemetryLabels creates cluster-style label maps rather than trusting collector labels.
func kubeTelemetryLabels(k *K3s, version bool) map[string]any {
	labels := map[string]any{}
	// Match the exact label representation produced by the Kubernetes JSON decoder.
	for key, value := range k.base.labels("demo") {
		labels[key] = value
	}
	if version { // Only runtime workloads carry the active deployment version.
		labels["io.hostlier.version"] = strings.Repeat("a", 64)
	}
	return labels
}

// TestK3sTelemetryResolvesOwnedPodComponents admits native sidecars without agent-specific metadata.
func TestK3sTelemetryResolvesOwnedPodComponents(t *testing.T) {
	k, attrs, _, _ := kubeTelemetryFixture(t)
	// Container samples use exact members; Pod aggregates retain an empty component identity.
	for _, component := range []string{"app", "worker", ""} {
		attrs["k8s.container.name"] = component
		attrs["hostlier.app"] = "forged"
		identity, err := k.ResolveTelemetry(context.Background(), attrs)
		if err != nil || identity.Application != "demo" || identity.Component != component || identity.Instance != attrs["k8s.pod.uid"] { // Claimed application labels are ignored.
			t.Fatalf("%s: %+v %v", component, identity, err)
		}
	}
}

// TestK3sTelemetryRechecksStateAfterInspection excludes a rollout concurrent with Kubernetes I/O.
func TestK3sTelemetryRechecksStateAfterInspection(t *testing.T) {
	k, attrs, _, _ := kubeTelemetryFixture(t)
	run := k.options.KubeRun
	k.options.KubeRun = func(ctx context.Context, args []string, input []byte) ([]byte, error) { // Finish a deployment before returning the observed Pod.
		if args[5] == "pod" { // Namespace discovery itself does not change the active deployment.
			advanceTelemetryRevision(t, k.base)
		}
		return run(ctx, args, input)
	}
	_, err := k.ResolveTelemetry(context.Background(), attrs)
	// Live ownership was valid at lookup start but the completed rollout invalidates attribution.
	if err == nil {
		t.Fatal("admitted stale revision after concurrent rollout")
	}
}

// TestK3sTelemetryRejectsUntrustedCoordinates tests each required identity dimension independently.
func TestK3sTelemetryRejectsUntrustedCoordinates(t *testing.T) {
	// Missing, mismatched, and option-like coordinates must fail before trusted attribution.
	for _, key := range []string{"k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid"} {
		for _, value := range []string{"", "foreign", "--all"} { // Include valid syntax that identifies the wrong object.
			k, attrs, _, _ := kubeTelemetryFixture(t)
			attrs[key] = value
			_, err := k.ResolveTelemetry(context.Background(), attrs)
			if err == nil { // Every coordinate is an independently verified constraint.
				t.Fatalf("accepted %s=%q", key, value)
			}
		}
	}
}

// TestK3sTelemetryRejectsUnknownComponents does not confuse Pod aggregates with named foreign members.
func TestK3sTelemetryRejectsUnknownComponents(t *testing.T) {
	// An omitted component is legitimate, but an explicit unknown or malformed component is not.
	for _, component := range []string{"foreign", "--all", "injected", "ephemeral-debugger"} {
		k, attrs, _, _ := kubeTelemetryFixture(t)
		attrs["k8s.container.name"] = component
		_, err := k.ResolveTelemetry(context.Background(), attrs)
		if err == nil { // Named components must remain declared application members.
			t.Fatalf("accepted component %q", component)
		}
	}
}

// TestK3sTelemetryRejectsForeignOrStaleObjects exercises actual cluster-object ownership boundaries.
func TestK3sTelemetryRejectsForeignOrStaleObjects(t *testing.T) {
	// A matching caller UID never bypasses current ownership, version, or component checks.
	for _, change := range []string{"namespace-owner", "pod-owner", "version", "uid", "namespace", "container", "init-job"} {
		k, attrs, ns, pod := kubeTelemetryFixture(t)
		alterTelemetryPod(change, ns, pod)
		_, err := k.ResolveTelemetry(context.Background(), attrs)
		if err == nil { // Reject even one mismatching trusted-runtime identity dimension.
			t.Fatal("accepted", change)
		}
	}
}

// alterTelemetryPod changes one trusted runtime observation for each rejection test.
func alterTelemetryPod(change string, ns, pod map[string]any) {
	meta := pod["metadata"].(map[string]any)
	labels := meta["labels"].(map[string]any)
	container := pod["spec"].(map[string]any)["initContainers"].([]any)[0].(map[string]any)
	switch change { // Keep single-property mutations independent so no missing check is masked.
	// A namespace owned by another controller cannot authorize a Pod.
	case "namespace-owner":
		ns["metadata"].(map[string]any)["labels"].(map[string]any)["io.hostlier.owner"] = "foreign"
	// A foreign Pod must be rejected even inside an owned namespace.
	case "pod-owner":
		labels["io.hostlier.owner"] = "foreign"
	// A retired rollout cannot impersonate the current runtime deployment.
	case "version":
		labels["io.hostlier.version"] = "previous"
	// Object replacement or cross-namespace coordinates invalidate attribution.
	case "uid", "namespace":
		meta[change] = "replaced"
	// An approved name must also occur in the actual Pod specification.
	case "container":
		container["name"] = "injected"
	// One-shot initialization must not be admitted as a persistent sidecar.
	case "init-job":
		delete(container, "restartPolicy")
	}
}
