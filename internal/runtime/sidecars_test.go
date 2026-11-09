package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// sidecarTransport executes the lifecycle fixture without opening a daemon socket.
type sidecarTransport func(*http.Request) (*http.Response, error)

// RoundTrip implements the private Docker HTTP transport for deterministic observations.
func (f sidecarTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// sidecarResponse encodes a bounded Docker API response for the fixture transport.
func sidecarResponse(value any) (*http.Response, error) {
	raw, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}

// sidecarLifecycle records whether the primary starts before its required dependency is ready.
type sidecarLifecycle struct {
	runtime *Runtime
	running map[string]bool
	ready   bool
	failed  bool
	started bool
	unsafe  bool
}

// request models a generic service probe and a primary process that depends on its readiness.
func (f *sidecarLifecycle) request(r *http.Request) (*http.Response, error) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Container endpoints only access the three owned group processes.
	if parts[0] == "containers" {
		return f.containerRequest(parts[1], parts[2])
	}
	// Only the dependency probe changes whether starting the primary is safe.
	if parts[0] == "exec" && parts[2] == "json" {
		exit := 0
		// Failed required probes must leave the primary stopped.
		if parts[1] == "backend" {
			f.ready = !f.failed
			if f.failed { // The outer deadline bounds repeated failures.
				exit = 1
			}
		}
		return sidecarResponse(map[string]any{"Running": false, "ExitCode": exit})
	}
	return sidecarResponse(map[string]any{})
}

// containerRequest keeps process start, inspection, and exec allocation state distinct.
func (f *sidecarLifecycle) containerRequest(id, action string) (*http.Response, error) {
	// Process start cannot imply dependency readiness.
	if action == "start" {
		f.running[id] = true
		if id == "primary" { // Record the externally observable ordering regression.
			f.started, f.unsafe = true, !f.ready
		}
	}
	// Exec allocation assigns a stable fixture identity to the owning process.
	if action == "exec" {
		return sidecarResponse(map[string]string{"Id": id})
	}
	return sidecarResponse(map[string]any{"Id": id, "Config": map[string]any{"Labels": f.runtime.labels("demo")}, "State": map[string]bool{"Running": f.running[id]}})
}

// TestDockerGenericSidecarRestartWaitsForReadiness prevents dependency races on start and restart.
func TestDockerGenericSidecarRestartWaitsForReadiness(t *testing.T) {
	// Both successful and permanently failing dependencies must respect primary startup ordering.
	for _, failed := range []bool{false, true} {
		f := &sidecarLifecycle{running: map[string]bool{}, failed: failed}
		r := &Runtime{owner: "test", options: Options{HealthTimeout: 25 * time.Millisecond}, docker: &http.Client{Transport: sidecarTransport(f.request)}}
		f.runtime = r
		probe := &model.Health{Exec: []string{"/app/ready"}}
		app := appRecord{Deployment: model.Deployment{Name: "demo", RuntimeID: "primary"}, Anchor: "network", Members: map[string]string{"backend": "backend"}, Spec: model.Project{Version: 2, Name: "demo", Workload: "worker", Endpoint: &model.Endpoint{Exposure: "none"}, Health: probe, Sidecars: map[string]model.Project{"backend": {Health: probe}}}}
		_, err := r.startGroup(context.Background(), app)
		// A successful dependency starts first; failed readiness never starts the primary.
		if f.unsafe || f.started == failed || (err != nil) != failed {
			t.Fatalf("failed=%v started=%v unsafe=%v err=%v", failed, f.started, f.unsafe, err)
		}
	}
}

// genericSidecarProject builds a framework-independent backend and listenerless helper.
func genericSidecarProject(t *testing.T) model.Project {
	t.Helper()
	image := "example.com/app@sha256:" + strings.Repeat("a", 64)
	p := model.Project{Version: 2, Name: "demo", Source: &model.Source{Image: image}, Endpoint: &model.Endpoint{Exposure: "public", Port: 8080, Route: "demo.example.com"}, Sidecars: map[string]model.Project{
		"backend": {Source: &model.Source{Image: image}, Endpoint: &model.Endpoint{Exposure: "loopback", Port: 8081}, Command: []string{"/app/backend"}, Args: []string{"--listen", "127.0.0.1:8081"}, Health: &model.Health{Exec: []string{"/app/probe"}}, Resources: &model.Limits{CPU: "0.1", Memory: "32Mi"}, Secrets: map[string]string{"TOKEN": "backend-token"}},
		"worker":  {Source: &model.Source{Image: image}, Workload: "worker", Command: []string{"/app/worker"}, Health: &model.Health{Exec: []string{"/app/ready"}}},
	}}
	p, err := config.ValidateProject(p)
	// Runtime tests must use the same normalized contract as an approved deployment.
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestK3sGenericSidecarsKeepProcessAndSecretBoundaries verifies ordinary applications use native sidecars.
func TestK3sGenericSidecarsKeepProcessAndSecretBoundaries(t *testing.T) {
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir()})
	// The manifest test needs no live cluster or image registry.
	if err != nil {
		t.Fatal(err)
	}
	p := genericSidecarProject(t)
	manifests, err := adapter.(*K3s).K3sManifests(p, model.DeployContext{OperationID: "generic", Secrets: map[string]string{"backend/TOKEN": "fixture"}})
	// Generic processes must render successfully without Harnest metadata.
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range manifests { // Inspect the shared Pod and the actual public service separately.
		if m["kind"] == "Deployment" {
			found = true
			checkGenericSidecarPod(t, p, m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any))
		}
		if m["kind"] == "Service" { // Sidecar endpoints must not be published by the group Service.
			raw, _ := json.Marshal(m)
			// Only the primary's HTTP port belongs in the publicly routed Service.
			if strings.Contains(string(raw), "8081") {
				t.Fatal("sidecar port published", string(raw))
			}
		}
	}
	// A manifest set without a runnable workload must never pass the contract test.
	if !found {
		t.Fatal("missing application Deployment")
	}
}

// checkGenericSidecarPod checks effective container launch vectors and credential references.
func checkGenericSidecarPod(t *testing.T, p model.Project, pod map[string]any) {
	t.Helper()
	items := pod["initContainers"].([]any)
	// Each generic sidecar is a persistent native sidecar in the primary's Pod.
	if len(items) != 2 || pod["automountServiceAccountToken"] != false {
		t.Fatal("incorrect group topology", pod)
	}
	for _, item := range items { // Every sidecar retains its own image and process contract.
		c := item.(map[string]any)
		member := p.Sidecars[c["name"].(string)]
		// Arbitrary executable commands must survive rendering without a framework wrapper.
		if c["restartPolicy"] != "Always" || !reflect.DeepEqual(c["command"], member.Command) || c["startupProbe"] == nil {
			t.Fatal("generic sidecar launch rewritten", c)
		}
	}
	worker := items[1].(map[string]any)
	// Worker sidecars inherit neither ports nor the backend's credentials.
	if worker["ports"] != nil || len(worker["env"].([]any)) != 0 {
		t.Fatal("worker inherited a sibling setting", worker)
	}
	checkGenericBackendSecret(t, items[0].(map[string]any))
}

// checkGenericBackendSecret verifies custom app arguments and a member-scoped Secret reference.
func checkGenericBackendSecret(t *testing.T, backend map[string]any) {
	t.Helper()
	env := backend["env"].([]any)
	// The authored credential must reach the backend through exactly one scoped reference.
	if len(env) != 1 {
		t.Fatal("missing backend credential", backend)
	}
	entry := env[0].(map[string]any)
	ref := entry["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]string)
	// The Kubernetes key uses a dot while preserving the internal backend/TOKEN scope.
	if entry["name"] != "TOKEN" || ref["key"] != "backend.TOKEN" {
		t.Fatal("incorrect credential scope", entry)
	}
	// Native sidecars retain arbitrary argument vectors and individual resource settings.
	if !reflect.DeepEqual(backend["args"], []string{"--listen", "127.0.0.1:8081"}) || backend["resources"] == nil {
		t.Fatal("missing backend process settings", backend)
	}
}
