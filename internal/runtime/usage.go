package runtime

import (
	"context"
	"hostlier/internal/model"
	"net/url"
	"time"
)

// usageRecord retrieves only a retained active application's approved desired settings.
func (r *Runtime) usageRecord(name string) (appRecord, model.Usage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.read()
	// Unreadable durable ownership state cannot authorize runtime observation.
	if err != nil {
		return appRecord{}, model.Usage{}, err
	}
	app, ok := state.Apps[name]
	// Deleted or unknown applications have no live workload metrics endpoint.
	if !ok {
		return app, model.Usage{}, model.Err("APP_NOT_FOUND", "Application not found")
	}
	usage := model.Usage{Limits: app.Spec.Resources, DesiredReplicas: model.DesiredReplicas(app.Spec), Status: app.Status}
	// The effective desired capacity of a sleeping service is zero.
	if app.IdleSleeping {
		usage.DesiredReplicas = 0
	}
	return app, usage, nil
}

// Usage observes actual Docker statistics after verifying the workload belongs to this application.
func (r *Runtime) Usage(ctx context.Context, name string) (model.Usage, error) {
	app, result, err := r.usageRecord(name)
	// Durable ownership and desired state are prerequisites for daemon queries.
	if err != nil {
		return result, err
	}
	result.Source = "docker-stats"
	current, err := r.container(ctx, app.RuntimeID, name)
	// A name or retained runtime ID alone never authorizes foreign workload metrics.
	if err != nil {
		return result, err
	}
	// Stopped applications have explicit unavailable measurements rather than fabricated zero usage.
	if !current.State.Running {
		result.Status = "stopped"
		result.Reason = "Workload is stopped"
		return result, nil
	}
	result.Status = "running"
	result.ReadyReplicas = 1
	return r.dockerUsage(ctx, current.ID, result)
}

// dockerUsage samples one owned workload and reads independently applied daemon limits.
func (r *Runtime) dockerUsage(ctx context.Context, id string, result model.Usage) (model.Usage, error) {
	var stats dockerStats
	path := "/containers/" + url.PathEscape(id)
	// A failed stats endpoint preserves desired state but reports no fabricated measurements.
	if r.api(ctx, "GET", path+"/stats?stream=false&one-shot=true", nil, &stats, nil) != nil {
		result.Reason = "Docker statistics are unavailable"
		return result, nil
	}
	stamp, stampErr := time.Parse(time.RFC3339Nano, stats.Read)
	// Invalid timestamps and overflowing counters cannot establish any usable resource observation.
	if stampErr != nil || stats.Memory.Usage > uint64(1<<63-1) {
		result.Reason = "Docker statistics are invalid"
		return result, nil
	}
	var limits dockerAppliedLimits
	// Applied limits must come from the daemon rather than copied project intent.
	if r.api(ctx, "GET", path+"/json", nil, &limits, nil) != nil {
		result.Reason = "Docker limits are unavailable"
		return result, nil
	}
	// One-shot reads avoid Docker's two-cycle wait; CPU deltas use the preceding capture for this exact container.
	cores, cpuAvailable := r.sampleDockerCPU(id, stats, stamp)
	result.Available = cpuAvailable
	// A first sample still supplies real memory while CPU waits for an authoritative counter delta.
	if cpuAvailable {
		result.CPUCores = &cores
	} else {
		result.Reason = "Waiting for the next Docker CPU sample"
	}
	memory := int64(stats.Memory.Usage)
	result.MemoryBytes = &memory
	result.CPUCapacityCores = float64(limits.HostConfig.NanoCPUs) / 1e9
	result.MemoryLimitBytes = limits.HostConfig.Memory
	result.ObservedAt = stamp
	return result, nil
}

// dockerCPUCores converts cumulative daemon counter deltas into consumed CPU cores.
func dockerCPUCores(stats dockerStats) (float64, bool) {
	// Missing or reset counters need another authoritative sample.
	if stats.Previous.System == 0 || stats.CPU.System <= stats.Previous.System || stats.CPU.CPUUsage.Total < stats.Previous.CPUUsage.Total || stats.CPU.Online == 0 {
		return 0, false
	}
	cpu := stats.CPU.CPUUsage.Total - stats.Previous.CPUUsage.Total
	system := stats.CPU.System - stats.Previous.System
	return float64(cpu) / float64(system) * float64(stats.CPU.Online), true
}
