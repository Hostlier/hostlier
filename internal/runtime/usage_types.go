package runtime

// dockerCPUCounter is Docker's cumulative CPU sample used to derive cores over the sample interval.
type dockerCPUCounter struct {
	CPUUsage struct {
		Total uint64 `json:"total_usage"`
	} `json:"cpu_usage"`
	System uint64 `json:"system_cpu_usage"`
	Online uint64 `json:"online_cpus"`
}

// dockerStats decodes only actual workload CPU and memory metrics returned by Docker.
type dockerStats struct {
	Read     string           `json:"read"`
	CPU      dockerCPUCounter `json:"cpu_stats"`
	Previous dockerCPUCounter `json:"precpu_stats"`
	Memory   struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
	} `json:"memory_stats"`
}

// dockerAppliedLimits captures effective daemon limits independently of the approved project record.
type dockerAppliedLimits struct {
	HostConfig struct {
		NanoCPUs int64 `json:"NanoCpus"`
		Memory   int64 `json:"Memory"`
	} `json:"HostConfig"`
}

// podMetricsList represents the namespaced metrics API response without Kubernetes client dependencies.
type podMetricsList struct {
	Items []podMetrics `json:"items"`
}

// podMetrics contains per-container resource usage and the sampled pod identity.
type podMetrics struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Timestamp  string             `json:"timestamp"`
	Containers []containerMetrics `json:"containers"`
}

// containerMetrics uses native Kubernetes quantity strings for CPU and working memory.
type containerMetrics struct {
	Usage map[string]string `json:"usage"`
}
