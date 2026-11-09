package controller

import (
	"context"
	"hostlier/internal/model"
	"testing"
)

// PlanResize supplies allocation capability to the recording resource adapter.
func (r *recordingResources) PlanResize(_ context.Context, s model.ResourceSpec) (model.ResourcePlan, error) {
	return model.ResourcePlan{Spec: s, Changes: map[string]any{"maintenance": "fixture database maintenance"}}, nil
}

// Resize records approved maintenance without introducing a second credential identity.
func (r *recordingResources) Resize(ctx context.Context, s model.ResourceSpec, c model.ResourceContext) (model.Resource, error) {
	return r.Provision(ctx, s, c)
}

// TestResourceResizeApproval verifies decline, stale approval and exact idempotent replay.
func TestResourceResizeApproval(t *testing.T) {
	m := &recordingResources{}
	c := resourceController(t, t.TempDir(), &recordingRuntime{}, m)
	defer c.Close(context.Background())
	_, create := call(t, c, true, "POST", "/v1/resource-plans", map[string]any{"spec": model.ResourceSpec{Name: "resize-db", Type: "postgres", Storage: "1Gi"}}, "")
	resourceApply(t, c, create, "create-resize-db")
	_, plan := call(t, c, true, "POST", "/v1/resource-plans/resize", map[string]any{"name": "resize-db", "cpu": "0.5", "memory": "256Mi"}, "")
	path := "/v1/plans/" + plan["id"].(string) + "/apply"
	status, _ := call(t, c, true, "POST", path, map[string]any{"approved": false, "fingerprint": plan["fingerprint"], "requestId": "decline"}, "")
	// Decline must leave allocation and adapter calls unchanged.
	if status == 200 || m.calls != 1 {
		t.Fatal("declined maintenance mutated resource")
	}
	c.mu.Lock()
	c.state.Generation++
	c.mu.Unlock()
	status, _ = call(t, c, true, "POST", path, map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": "stale"}, "")
	// Changing controller state invalidates allocation approval.
	if status == 200 {
		t.Fatal("stale plan accepted")
	}
	_, plan = call(t, c, true, "POST", "/v1/resource-plans/resize", map[string]any{"name": "resize-db", "cpu": "0.5", "memory": "256Mi"}, "")
	completed := resourceApply(t, c, plan, "resize-approved")
	// Allocation success must not attempt to record an application revision.
	if completed["status"] != "succeeded" {
		t.Fatal(completed)
	}
	checkResizeReplay(t, c, m, plan)
}

// checkResizeReplay checks durable desired state and one operation for exact request replay.
func checkResizeReplay(t *testing.T, c *Controller, m *recordingResources, plan map[string]any) {
	t.Helper()
	path := "/v1/plans/" + plan["id"].(string) + "/apply"
	status, _ := call(t, c, true, "POST", path, map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": "resize-approved"}, "")
	// Replaying the same approval returns its original operation without engine mutation.
	if status != 200 || m.calls != 2 {
		t.Fatal("replay mutated database", status, m.calls)
	}
	resource := c.state.Resources["resize-db"]
	// Allocation updates retain identity and storage while committing desired CPU and memory.
	if resource.Spec.CPU != "0.5" || resource.Spec.Memory != "256Mi" || resource.Spec.Storage != "1Gi" {
		t.Fatal(resource.Spec)
	}
	status, _ = call(t, c, true, "POST", path, map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": "different-replay"}, "")
	// A second request identity cannot reuse consumed allocation authority.
	if status == 200 {
		t.Fatal("consumed plan accepted a second request")
	}
}
