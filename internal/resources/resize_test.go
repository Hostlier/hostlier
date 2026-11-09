package resources

import (
	"context"
	"hostlier/internal/model"
	"testing"
)

// TestResizePlanPreservesIdentity rejects allocation requests that alter durable storage.
func TestResizePlanPreservesIdentity(t *testing.T) {
	manager, err := New(Options{StateDir: t.TempDir()})
	// Require the selected resource backend to initialize successfully.
	if err != nil {
		t.Fatal(err)
	}
	m := manager.(*Manager)
	prior := model.ResourceSpec{Name: "db", Type: "postgres", Storage: "1Gi", Image: Images["postgres"]}
	// Retain fixture metadata before testing allocation invariants.
	if err := m.save(map[string]record{"db": {Resource: model.Resource{Name: "db", Type: "postgres", Status: "running"}, Spec: prior}}); err != nil {
		t.Fatal(err)
	}
	next := prior
	next.CPU = "0.5"
	next.Memory = "256Mi"
	// Read-only planning must validate allocation before any runtime mutation.
	if _, err := m.PlanResize(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	next.Storage = "2Gi"
	// Read-only planning must validate allocation before any runtime mutation.
	if _, err := m.PlanResize(context.Background(), next); err == nil {
		t.Fatal("accepted storage change")
	}
}
