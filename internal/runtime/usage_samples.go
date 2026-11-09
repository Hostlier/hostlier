package runtime

import "time"

// dockerUsageSample retains only bounded counters for a verified full container identity, never workload secrets.
type dockerUsageSample struct {
	counter   dockerCPUCounter
	observed  time.Time
	cores     float64
	available bool
}

// sampleDockerCPU derives CPU from successive one-shot captures without waiting in Docker for another collection cycle.
func (r *Runtime) sampleDockerCPU(id string, stats dockerStats, stamp time.Time) (float64, bool) {
	r.usageMu.Lock()
	defer r.usageMu.Unlock()
	previous, exists := r.usageSamples[id]
	// Out-of-order concurrent requests cannot replace the newest baseline or fabricate a fresh measurement.
	if exists && stamp.Before(previous.observed) {
		return 0, false
	}
	// Docker can serve the same sampled timestamp to simultaneous status and usage consumers.
	if exists && stamp.Equal(previous.observed) {
		// Reuse a calculated delta only when this is the identical counter sample.
		if stats.CPU.System == previous.counter.System && stats.CPU.CPUUsage.Total == previous.counter.CPUUsage.Total {
			return previous.cores, previous.available
		}
		return 0, false
	}
	cores, ok := dockerCPUCores(stats)
	// Some daemons provide a preceding counter; otherwise use our last accepted capture for this container only.
	if !ok && exists {
		stats.Previous = previous.counter
		cores, ok = dockerCPUCores(stats)
	}
	r.rememberDockerCPU(id, dockerUsageSample{counter: stats.CPU, observed: stamp, cores: cores, available: ok})
	return cores, ok
}

// rememberDockerCPU evicts the oldest retained container baseline while the independent sampling mutex is held.
func (r *Runtime) rememberDockerCPU(id string, sample dockerUsageSample) {
	// Lazy allocation also supports test runtimes constructed without the public constructor.
	if r.usageSamples == nil {
		r.usageSamples = make(map[string]dockerUsageSample)
	}
	_, exists := r.usageSamples[id]
	// A rolling deployment must not grow observation memory indefinitely with retired container IDs.
	if !exists && len(r.usageSamples) >= 256 {
		oldest := ""
		var stamp time.Time
		// Retain the most recently observed workloads when historical baselines reach their fixed capacity.
		for key, value := range r.usageSamples {
			// The first candidate seeds the minimum timestamp without a separate allocation or sorting pass.
			if oldest == "" || value.observed.Before(stamp) {
				oldest = key
				stamp = value.observed
			}
		}
		delete(r.usageSamples, oldest)
	}
	r.usageSamples[id] = sample
}
