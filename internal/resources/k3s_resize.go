package resources

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"time"
)

// PlanResize previews Kubernetes Recreate maintenance without changing PVC or Secret objects.
func (m *K3sManager) PlanResize(ctx context.Context, next model.ResourceSpec) (model.ResourcePlan, error) {
	prior, err := m.find(next.Name)
	// Existing retained state defines all immutable settings.
	if err != nil {
		return model.ResourcePlan{}, err
	}
	// Reject allocation intent that changes immutable resource settings.
	if err = resizeSpec(prior.Spec, next); err != nil {
		return model.ResourcePlan{}, err
	}
	return model.ResourcePlan{Spec: next, Changes: map[string]any{"action": "resize", "before": prior.Spec, "after": next, "maintenance": "Recreate database pod with new CPU and memory; dependent applications may see interruption", "data": "PVC, image, Secret and connection identity retained; previous limits restored on failure"}}, nil
}

// patchAllocation changes only the owned engine container's allocation via a merge patch.
func (m *K3sManager) patchAllocation(ctx context.Context, name string, allocation any) error {
	patch := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "engine", "resources": allocation}}}}}}
	data, err := json.Marshal(patch)
	// Structured patches cannot alter storage, credentials or unrelated fields.
	if err != nil {
		return err
	}
	_, err = m.run(ctx, []string{"patch", "deployment", "engine", "-n", m.namespace(name), "--type=strategic", "--patch", string(data)}, nil)
	return err
}

// engineAllocation reads the exact allocation of the sole database engine container.
func engineAllocation(deployment map[string]any) (any, error) {
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	pod, _ := template["spec"].(map[string]any)
	containers, _ := pod["containers"].([]any)
	// A modified workload shape cannot be safely resized.
	if len(containers) != 1 {
		return nil, model.Err("RESOURCE_RESIZE", "Expected exactly one database engine container")
	}
	engine, _ := containers[0].(map[string]any)
	// Only the expected database engine container may receive new limits.
	if engine["name"] != "engine" {
		return nil, model.Err("RESOURCE_RESIZE", "Database engine container unavailable")
	}
	return engine["resources"], nil
}

// Resize rolls out an allocation-only patch and restores old allocation on any failure.
func (m *K3sManager) Resize(ctx context.Context, next model.ResourceSpec, c model.ResourceContext) (model.Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Validate immutable settings before any cluster write.
	if _, err := m.PlanResize(ctx, next); err != nil {
		return model.Resource{}, err
	}
	state, err := m.read()
	// Require successful resource observation before advancing maintenance.
	if err != nil {
		return model.Resource{}, err
	}
	prior := state[next.Name]
	old, err := m.ownedAllocation(ctx, next.Name)
	// A verified owned workload is required before applying allocation changes.
	if err != nil {
		return model.Resource{}, err
	}
	allocation := map[string]any{"limits": map[string]string{"cpu": next.CPU, "memory": next.Memory}, "requests": map[string]string{"cpu": next.CPU, "memory": next.Memory}}
	err = m.patchAllocation(ctx, next.Name, allocation)
	// Wait for Kubernetes replacement before authenticating the database.
	if err == nil {
		_, err = m.run(ctx, []string{"rollout", "status", "deployment/engine", "-n", prior.Network, "--timeout=180s"}, nil)
	}
	// Advance only after the preceding maintenance step succeeded.
	if err == nil {
		err = m.ready(ctx, prior, c.Credentials)
	}
	// Advance only after the preceding maintenance step succeeded.
	if err == nil {
		prior.Spec = next
		state[next.Name] = prior
		err = m.save(state)
	}
	// Failed changes restore the old pod allocation while retaining its PVC and Secret.
	if err != nil {
		// Report incomplete rollback explicitly while retaining database storage.
		if restoreErr := m.restoreAllocation(next.Name, old, prior, c.Credentials); restoreErr != nil {
			return model.Resource{}, restoreErr
		}
		return model.Resource{}, err
	}
	return prior.Resource, nil
}

// restoreAllocation restores the previous exact resources map with a fresh bounded context.
func (m *K3sManager) restoreAllocation(name string, old any, prior record, credentials model.ResourceCredentials) error {
	rollback, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Clearing first prevents strategic merge from retaining newly added fields.
	if err := m.patchAllocation(rollback, name, nil); err != nil {
		return model.Err("RESOURCE_RESIZE_ROLLBACK", "Resize failed and clearing changed allocation failed; data retained")
	}
	// The previous request and limit map is restored without replacing the PVC.
	if err := m.patchAllocation(rollback, name, old); err != nil {
		return model.Err("RESOURCE_RESIZE_ROLLBACK", "Resize failed and restoring previous allocation failed; data retained")
	}
	// Verify the restored allocation has actually produced a ready replacement pod.
	if _, err := m.run(rollback, []string{"rollout", "status", "deployment/engine", "-n", prior.Network, "--timeout=180s"}, nil); err != nil {
		return model.Err("RESOURCE_RESIZE_ROLLBACK", "Previous allocation restored but rollout failed; data retained")
	}
	// Authenticated readiness confirms recovery of the existing database credentials.
	if err := m.ready(rollback, prior, credentials); err != nil {
		return model.Err("RESOURCE_RESIZE_ROLLBACK", "Previous allocation restored but readiness failed; data retained")
	}
	return nil
}

// ownedAllocation verifies ownership and snapshots allocation before a maintenance write.
func (m *K3sManager) ownedAllocation(ctx context.Context, name string) (any, error) {
	deployment, err := m.get(ctx, "deployment", "engine", m.namespace(name))
	// Failed observations cannot authorize maintenance.
	if err != nil {
		return nil, err
	}
	// Refuse changes to foreign deployments even in an expected namespace.
	if !m.owned(deployment, name) {
		return nil, model.Err("RESOURCE_OWNERSHIP", "Refusing to resize unowned database")
	}
	return engineAllocation(deployment)
}
