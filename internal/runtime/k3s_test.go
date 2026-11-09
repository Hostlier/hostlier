package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// TestK3sManifestIsolation verifies private networking, scoped secrets and approved workload limits.
func TestK3sManifestIsolation(t *testing.T) {
	adapter, e := New(Options{Type: "k3s", StateDir: "/var/lib/test-hostlier"})
	// Require the Kubernetes adapter to construct without a running cluster.
	if e != nil {
		t.Fatal(e)
	}
	k := adapter.(*K3s)
	spec := model.Project{Name: "app", Image: "image@sha256:" + strings.Repeat("a", 64), Port: 3000, Route: "app.example.com", Resources: &model.Limits{CPU: "0.5", Memory: "512Mi"}, Volumes: map[string]model.Volume{"data": {Mount: "/data", Size: "5Gi"}}}
	manifests, e := k.K3sManifests(spec, model.DeployContext{OperationID: "approved-op", Secrets: map[string]string{"API_KEY": "fixture-secret"}, DependencyNetworks: []string{"hostlier-" + k.base.owner + "-resource-db"}})
	// Require manifest generation for an approved immutable application.
	if e != nil {
		t.Fatal(e)
	}
	// Install isolation before any workload object can start.
	if manifests[0]["kind"] != "NetworkPolicy" {
		t.Fatal("workload starts before isolation")
	}
	raw, _ := json.Marshal(manifests)
	// Check every persistence, credential and resource-limit property in the manifest set.
	for _, field := range []string{`"kind":"PersistentVolumeClaim"`, `"storageClassName":"local-path"`, `"automountServiceAccountToken":false`, `"kind":"Secret"`, `"stringData":{"API_KEY":"fixture-secret"}`, `"type":"Recreate"`, `"limits":{"cpu":"0.5","memory":"512Mi"}`} {
		// A missing required field weakens the declared workload contract.
		if !strings.Contains(string(raw), field) {
			t.Error("missing manifest capability", field)
		}
	}
	// Exclude host networking, public ports and privileged container settings.
	for _, forbidden := range []string{`"hostPort"`, `"hostNetwork"`, `"NodePort"`, `"privileged"`} {
		// Any forbidden field would bypass private workload isolation.
		if strings.Contains(string(raw), forbidden) {
			t.Error("public/privileged workload", forbidden)
		}
	}
	// Reject egress binding to an unowned resource namespace.
	if _, e = k.K3sManifests(spec, model.DeployContext{OperationID: "op", DependencyNetworks: []string{"unowned-resource"}}); e == nil {
		t.Fatal("unowned dependency accepted")
	}
}

// TestK3sExplicitPlacementAndOwnership rejects foreign objects using explicit kubeconfig placement.
func TestK3sExplicitPlacementAndOwnership(t *testing.T) {
	calls := []string{}
	// Capture the exact kubectl arguments while returning an externally owned namespace.
	adapter, e := New(Options{Type: "k3s", StateDir: "/var/lib/hostlier-test", Kubeconfig: "/etc/rancher/k3s/k3s.yaml", KubeRun: func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte(`{"metadata":{"labels":{"io.hostlier.owner":"unrelated"}}}`), nil
	}})
	// Require adapter construction using the injected command runner.
	if e != nil {
		t.Fatal(e)
	}
	k := adapter.(*K3s)
	e = k.namespaceReady(context.Background(), "app")
	// Applying over a foreign namespace must fail ownership validation.
	if e == nil {
		t.Fatal("adopted unowned namespace")
	}
	// The lookup must use the configured kubeconfig and explicit default context.
	if len(calls) != 1 || !strings.Contains(calls[0], "--kubeconfig=/etc/rancher/k3s/k3s.yaml --context=default") {
		t.Fatal(calls)
	}
}
