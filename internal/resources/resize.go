package resources

import (
	"context"
	"math"
	"net/url"
	"strconv"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// resizeSpec restricts maintenance to finite CPU and positive memory allocations.
func resizeSpec(prior, next model.ResourceSpec) error {
	cpu, err := strconv.ParseFloat(next.CPU, 64)
	// Both allocations must be explicit and share the runtimes' millicore precision.
	if err != nil || math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu < 0.001 || cpu > 1024 || math.Abs(cpu*1000-math.Round(cpu*1000)) > 0.000001 {
		return model.Err("RESOURCE_ALLOCATION", "CPU must be 0.001 to 1024 cores in increments of 0.001")
	}
	// Memory is checked before any runtime mutation.
	if _, err := config.ParseMemory(next.Memory); err != nil {
		return err
	}
	prior.CPU = next.CPU
	prior.Memory = next.Memory
	prior.PackageDigest = next.PackageDigest
	// The approved change cannot alter image, credentials, volume size or identity.
	if prior != next {
		return model.Err("RESOURCE_RESIZE", "Resize may change only CPU and memory")
	}
	return nil
}

// PlanResize describes a data-preserving allocation change to an existing Docker instance.
func (m *Manager) PlanResize(ctx context.Context, next model.ResourceSpec) (model.ResourcePlan, error) {
	prior, err := m.find(next.Name)
	// Only retained resources can be resized.
	if err != nil {
		return model.ResourcePlan{}, err
	}
	// Validate immutable fields before returning a maintenance preview.
	if err = resizeSpec(prior.Spec, next); err != nil {
		return model.ResourcePlan{}, err
	}
	return model.ResourcePlan{Spec: next, Changes: map[string]any{"action": "resize", "before": prior.Spec, "after": next, "maintenance": "Live Docker allocation update; lowering memory can terminate the database; maintenance approval required", "data": "Existing volume, image and credentials retained; failed changes restore previous limits"}}, nil
}

// dockerAllocation translates approved quantities to Docker's native update fields.
func dockerAllocation(spec model.ResourceSpec) map[string]any {
	cpu, _ := strconv.ParseFloat(spec.CPU, 64)
	memory, _ := config.ParseMemory(spec.Memory)
	return map[string]any{"NanoCpus": int64(math.Round(cpu * 1e9)), "Memory": memory, "MemorySwap": int64(-1)}
}

// Resize changes owned container limits, verifies authentication and persists desired allocation.
func (m *Manager) Resize(ctx context.Context, next model.ResourceSpec, c model.ResourceContext) (model.Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan, err := m.PlanResize(ctx, next)
	// Invalid plans never reach a mutable Docker endpoint.
	if err != nil {
		return model.Resource{}, err
	}
	state, prior, err := m.resizeRecord(next.Name)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return model.Resource{}, err
	}
	current, err := m.inspect(ctx, next.Name)
	// Ownership and a running instance are prerequisites for authenticated verification.
	if err != nil {
		return model.Resource{}, err
	}
	// Authenticated maintenance requires an already running database.
	if !current.State.Running {
		return model.Resource{}, model.Err("RESOURCE_STOPPED", "Start the resource before resizing")
	}
	path := "/containers/" + url.PathEscape(current.ID) + "/update"
	_, err = m.api(ctx, "POST", path, dockerAllocation(plan.Spec), nil)
	// Readiness catches OOM exits and failed authenticated engine recovery.
	if err == nil {
		err = m.ready(ctx, next, c.Credentials)
	}
	// Runtime failure restores the exact previously observed allocation.
	if err != nil {
		// Restore enforcement and authenticated service before reporting the original failure.
		if restoreErr := m.restoreDockerAllocation(current, prior.Spec, c.Credentials); restoreErr != nil {
			return model.Resource{}, restoreErr
		}
		return model.Resource{}, err
	}
	prior.Spec = next
	state[next.Name] = prior
	// Persistence failure must restore runtime limits before reporting failure.
	if err = m.save(state); err != nil {
		// Restore enforcement and authenticated service before reporting the original failure.
		if restoreErr := m.restoreDockerAllocation(current, prior.Spec, c.Credentials); restoreErr != nil {
			return model.Resource{}, restoreErr
		}
		return model.Resource{}, err
	}
	return prior.Resource, nil
}

// restoreDockerAllocation restores observed limits and authenticates the retained database again.
func (m *Manager) restoreDockerAllocation(current container, spec model.ResourceSpec, c model.ResourceCredentials) error {
	rollback, cancel := context.WithTimeout(context.Background(), m.options.HealthTimeout)
	defer cancel()
	allocation := map[string]any{}
	// Restore only mutable allocation fields, never unrelated container settings.
	for _, key := range []string{"NanoCpus", "CpuPeriod", "CpuQuota", "Memory", "MemorySwap", "MemoryReservation"} {
		allocation[key] = current.HostConfig[key]
	}
	_, err := m.api(rollback, "POST", "/containers/"+url.PathEscape(current.ID)+"/update", allocation, nil)
	// Limit rollback failure is distinct from the original maintenance error.
	if err != nil {
		return model.Err("RESOURCE_RESIZE_ROLLBACK", "Resize failed and restoring previous limits failed; data retained")
	}
	// An OOM-killed engine may need restarting with its original allocation.
	if _, err = m.api(rollback, "POST", "/containers/"+url.PathEscape(current.ID)+"/start", nil, nil); err != nil {
		return model.Err("RESOURCE_RESIZE_ROLLBACK", "Previous limits restored but database restart failed; data retained")
	}
	// Report rollback success only after authenticating the restored instance.
	if err = m.ready(rollback, spec, c); err != nil {
		return model.Err("RESOURCE_RESIZE_ROLLBACK", "Previous limits restored but database readiness failed; data retained")
	}
	return nil
}

// resizeRecord refuses allocation changes to a database reserved by a recovery workflow.
func (m *Manager) resizeRecord(name string) (map[string]record, record, error) {
	state, err := m.read()
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, record{}, err
	}
	prior := state[name]
	// Reject this request: Database is reserved by a recovery workflow.
	if prior.RecoveryFence != "" {
		return nil, record{}, model.Err("RECOVERY_FENCE", "Database is reserved by a recovery workflow")
	}
	return state, prior, nil
}
