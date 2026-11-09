package controller

import (
	"context"
	"testing"

	"hostlier/internal/model"
)

// inventoryResources counts runtime inspection separately from retained metadata reads.
type inventoryResources struct {
	recordingResources
	observations int
}

// Status records an expensive backend probe so metadata inventories can prove they do not invoke it.
func (r *inventoryResources) Status(context.Context, string) (model.Resource, error) {
	r.observations++
	return model.Resource{Name: "cache", Status: "running"}, nil
}

// TestMonitoringInventoriesAvoidProbesAndBackupPayloads preserves read-only resource and recovery visibility without expensive runtime fanout.
func TestMonitoringInventoriesAvoidProbesAndBackupPayloads(t *testing.T) {
	manager := &inventoryResources{}
	c, err := New(Options{StateDir: t.TempDir(), Runtime: &recordingRuntime{}, ResourceManager: manager, Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}})
	// Valid controller ownership is required before exercising inventory routes.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeController(t, c) }) // Release the fixture store and observation worker after all reads finish.
	c.mu.Lock()
	c.state.Resources["cache"] = &ResourceRecord{Resource: model.Resource{Name: "cache", Type: "redis", Status: "running"}, Spec: model.ResourceSpec{Name: "cache", Type: "redis", Storage: "1Gi"}}
	c.state.Backups["receipt"] = BackupRecord{ID: "receipt", Name: "cache", Type: "redis", Size: 100}
	if err := c.store.Save(c.state); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	status, result := call(t, c, true, "GET", "/v1/resources?observed=false", nil, "")
	// An inventory read must return retained rows without a single backend status request.
	if status != 200 || len(result["resources"].([]any)) != 1 || manager.observations != 0 {
		t.Fatal(status, result, manager.observations)
	}
	status, result = call(t, c, true, "GET", "/v1/backups", nil, "")
	// Recovery monitoring exposes receipt metadata without requesting any backup export or restore.
	if status != 200 || result["backups"] == nil {
		t.Fatal(status, result)
	}
	status, _ = call(t, c, true, "GET", "/v1/resources/cache", nil, "")
	// Fresh inspection remains available when a user opens exactly one resource detail page.
	if status != 200 || manager.observations != 1 {
		t.Fatal(status, manager.observations)
	}
}
