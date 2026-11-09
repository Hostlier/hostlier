package runtime

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"math"
	"strconv"
	"strings"
	"time"
)

// Usage checks current workload ownership before querying optional namespaced Kubernetes metrics.
func (k *K3s) Usage(ctx context.Context, name string) (model.Usage, error) {
	app, result, err := k.base.usageRecord(name)
	// Persistent desired state must be readable before cluster observation.
	if err != nil {
		return result, err
	}
	result.Source = "kubernetes-metrics"
	namespace, err := k.get(ctx, "namespace", k.namespace(name), "")
	// Namespace ownership confines all subsequent metrics to this server and application.
	if err != nil {
		return result, err
	}
	// A foreign or absent namespace cannot authorize metric collection.
	if !k.usageOwned(namespace, name) {
		return result, model.Err("OWNERSHIP", "Application namespace ownership could not be verified")
	}
	deployment, err := k.get(ctx, "deployment", app.RuntimeID, k.namespace(name))
	// The current recorded deployment is the sole admitted version.
	if err != nil {
		return result, err
	}
	// Never read another application's workload based only on a reused deployment name.
	if !k.usageOwned(deployment, name) {
		return result, model.Err("OWNERSHIP", "Application deployment ownership could not be verified")
	}
	result = k.observedDeploymentUsage(result, deployment)
	// Zero or stopped workloads report unavailable metrics explicitly.
	if result.ReadyReplicas == 0 {
		result.Reason = "No ready workload replicas"
		return result, nil
	}
	return k.k3sUsageMetrics(ctx, name, app.RuntimeID, result)
}

// usageOwned requires the full server and application ownership label tuple.
func (k *K3s) usageOwned(object map[string]any, name string) bool {
	metadata, _ := object["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	return labels["io.hostlier.managed"] == "true" && labels["io.hostlier.owner"] == k.base.owner && labels["io.hostlier.app"] == name
}

// observedDeploymentUsage reads rollout counters and effective per-replica limits from actual cluster state.
func (k *K3s) observedDeploymentUsage(result model.Usage, deployment map[string]any) model.Usage {
	status, _ := deployment["status"].(map[string]any)
	ready, _ := status["readyReplicas"].(float64)
	result.ReadyReplicas = int(ready)
	limits := deploymentLimits(deployment)
	result.CPUCapacityCores, _ = cpuQuantity(limits["cpu"])
	result.MemoryLimitBytes, _ = ParseMemory(limits["memory"])
	return result
}

// deploymentLimits extracts actual app-container limits without treating desired project values as applied state.
func deploymentLimits(deployment map[string]any) map[string]string {
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	// A missing container means no observed effective limit evidence.
	if len(containers) == 0 {
		return nil
	}
	container, _ := containers[0].(map[string]any)
	resources, _ := container["resources"].(map[string]any)
	limits, _ := resources["limits"].(map[string]any)
	cpu, _ := limits["cpu"].(string)
	memory, _ := limits["memory"].(string)
	return map[string]string{"cpu": cpu, "memory": memory}
}

// k3sUsageMetrics excludes retained versions and unrelated pods before aggregating resource measurements.
func (k *K3s) k3sUsageMetrics(ctx context.Context, name, version string, result model.Usage) (model.Usage, error) {
	pods, err := k.currentUsagePods(ctx, name, version)
	// Missing or malformed pod observations cannot prove which metrics belong to the active revision.
	if err != nil {
		result.Reason = "Current workload pods are unavailable"
		return result, nil
	}
	raw, err := k.run(ctx, []string{"get", "--raw=/apis/metrics.k8s.io/v1beta1/namespaces/" + k.namespace(name) + "/pods"}, nil)
	// Metrics-server is optional; its absence is never represented as measured zero.
	if err != nil {
		result.Reason = "Kubernetes metrics API is unavailable"
		return result, nil
	}
	var metrics podMetricsList
	// Malformed measurements cannot be reported as an authoritative sample.
	if json.Unmarshal(raw, &metrics) != nil {
		result.Reason = "Kubernetes metrics response is invalid"
		return result, nil
	}
	return aggregatePodUsage(metrics, pods, result), nil
}

// currentUsagePods verifies server, application and version ownership from actual pod records.
func (k *K3s) currentUsagePods(ctx context.Context, name, version string) (map[string]bool, error) {
	raw, err := k.run(ctx, []string{"get", "pods", "-n", k.namespace(name), "-l", "io.hostlier.version=" + version, "-o", "json"}, nil)
	// Cluster lookup failures prevent pod metric admission.
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	// Only valid namespaced pod lists may establish metric identities.
	if json.Unmarshal(raw, &list) != nil {
		return nil, model.Err("METRICS", "Invalid workload pod response")
	}
	result := map[string]bool{}
	// Retained and foreign pods cannot enter the active-version metric aggregate.
	for _, pod := range list.Items {
		metadata, _ := pod["metadata"].(map[string]any)
		labels, _ := metadata["labels"].(map[string]any)
		podName, _ := metadata["name"].(string)
		// Full owner identity, current revision and nonterminating status are all required.
		if !k.usageOwned(pod, name) || labels["io.hostlier.version"] != version || metadata["deletionTimestamp"] != nil || podName == "" {
			continue
		}
		result[podName] = true
	}
	return result, nil
}

// aggregatePodUsage requires a complete current-pod sample; partial or malformed samples remain unavailable.
func aggregatePodUsage(metrics podMetricsList, pods map[string]bool, result model.Usage) model.Usage {
	totalCPU, totalMemory := float64(0), int64(0)
	observed := map[string]bool{}
	// Ignore metrics from old deployment revisions and unrelated namespaced workloads.
	for _, pod := range metrics.Items {
		// Each verified pod contributes at most once to the aggregate.
		if !pods[pod.Metadata.Name] || observed[pod.Metadata.Name] {
			continue
		}
		cpu, memory, ok := containerUsage(pod.Containers)
		// One malformed or missing container measurement invalidates the aggregate.
		if !ok {
			result.Reason = "Workload resource sample is unavailable"
			return result
		}
		totalCPU += cpu
		totalMemory += memory
		observed[pod.Metadata.Name] = true
		stamp, err := time.Parse(time.RFC3339Nano, pod.Timestamp)
		// Invalid collection timestamps cannot establish a current authoritative sample.
		if err != nil {
			result.Reason = "Workload metric timestamp is invalid"
			return result
		}
		// Report the oldest included observation rather than implying fresher aggregate data.
		if result.ObservedAt.IsZero() || stamp.Before(result.ObservedAt) {
			result.ObservedAt = stamp
		}
	}
	// Absence and partial collection are explicit unavailable states.
	if len(pods) == 0 || len(observed) != len(pods) {
		result.Reason = "Workload metrics sample is incomplete"
		return result
	}
	result.Available = true
	result.CPUCores = &totalCPU
	result.MemoryBytes = &totalMemory
	return result
}

// containerUsage converts a pod's native metric quantities without substituting missing data with zero.
func containerUsage(containers []containerMetrics) (float64, int64, bool) {
	// A pod without a container sample has no measured usage.
	if len(containers) == 0 {
		return 0, 0, false
	}
	cpu, memory := float64(0), int64(0)
	// Aggregate every measured container of an admitted workload pod.
	for _, container := range containers {
		cores, err := cpuQuantity(container.Usage["cpu"])
		bytes, memErr := metricMemory(container.Usage["memory"])
		// Invalid quantities invalidate the whole pod sample.
		if err != nil || memErr != nil {
			return 0, 0, false
		}
		cpu += cores
		memory += bytes
	}
	return cpu, memory, true
}

// cpuQuantity parses the CPU unit forms emitted by Kubernetes metrics-server.
func cpuQuantity(raw string) (float64, error) {
	factor := float64(1)
	// CPU measurements use nanocores, microcores, millicores, or whole cores.
	for suffix, multiplier := range map[string]float64{"n": 1e-9, "u": 1e-6, "m": 1e-3} {
		// Consume exactly one recognized unit suffix.
		if strings.HasSuffix(raw, suffix) {
			factor = multiplier
			raw = strings.TrimSuffix(raw, suffix)
			break
		}
	}
	value, err := strconv.ParseFloat(raw, 64)
	// Nonfinite or negative quantities cannot represent valid workload consumption.
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, model.Err("METRICS", "Invalid CPU quantity")
	}
	return value * factor, nil
}

// metricMemory accepts genuine measured zero while reusing the runtime's validated byte quantity parser.
func metricMemory(raw string) (int64, error) {
	// Zero consumption is real data and must remain distinguishable from unavailable metrics.
	if raw == "0" {
		return 0, nil
	}
	return ParseMemory(raw)
}
