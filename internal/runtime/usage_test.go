package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestDockerCPUUsageDerivesCores verifies actual counter deltas rather than desired CPU settings.
func TestDockerCPUUsageDerivesCores(t *testing.T) {
	var stats dockerStats
	json.Unmarshal([]byte(`{"cpu_stats":{"cpu_usage":{"total_usage":150},"system_cpu_usage":1200,"online_cpus":4},"precpu_stats":{"cpu_usage":{"total_usage":100},"system_cpu_usage":1000}}`), &stats)
	cores, ok := dockerCPUCores(stats)
	// Fifty workload ticks across two hundred system ticks on four CPUs equals one core.
	if !ok || cores != 1 {
		t.Fatal(cores, ok)
	}
	stats.CPU.CPUUsage.Total = 100
	cores, ok = dockerCPUCores(stats)
	// A real idle sample remains valid measured zero.
	if !ok || cores != 0 {
		t.Fatal("idle measurement unavailable")
	}
	stats.CPU.System = 0
	// Counter resets cannot fabricate measured zero.
	if _, ok = dockerCPUCores(stats); ok {
		t.Fatal("counter reset accepted")
	}
}

// TestK3sUsageMetricsFiltersAndQuantities covers complete current samples, foreign versions and native units.
func TestK3sUsageMetricsFiltersAndQuantities(t *testing.T) {
	var metrics podMetricsList
	json.Unmarshal([]byte(`{"items":[{"metadata":{"name":"current"},"timestamp":"2026-10-29T12:00:00Z","containers":[{"usage":{"cpu":"125000000n","memory":"2Mi"}},{"usage":{"cpu":"25m","memory":"512Ki"}}]},{"metadata":{"name":"old"},"containers":[{"usage":{"cpu":"99","memory":"99Gi"}}]}]}`), &metrics)
	result := aggregatePodUsage(metrics, map[string]bool{"current": true}, model.Usage{})
	// Retained-version metrics must not inflate the current deployment's measured usage.
	if !result.Available || result.CPUCores == nil || *result.CPUCores != 0.15 || result.MemoryBytes == nil || *result.MemoryBytes != 2621440 {
		t.Fatal(result)
	}
	result = aggregatePodUsage(metrics, map[string]bool{"current": true, "missing": true}, model.Usage{})
	// Partial pod measurements remain explicit unavailable observations.
	if result.Available || result.CPUCores != nil || result.Reason == "" {
		t.Fatal("partial metrics accepted")
	}
}

// TestK3sUsageUnavailableAndOwnership exercises the real observer against injected cluster responses.
func TestK3sUsageUnavailableAndOwnership(t *testing.T) {
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir()})
	// Runtime construction must not require a live metrics-server.
	if err != nil {
		t.Fatal(err)
	}
	k := adapter.(*K3s)
	app := appRecord{Deployment: model.Deployment{Name: "app", RuntimeID: "current", Status: "running"}, Spec: model.Project{Name: "app", Resources: &model.Limits{CPU: "0.5", Memory: "64Mi"}}}
	// Retain an actual approved desired record for the public usage contract.
	if err = k.base.save(state{Apps: map[string]appRecord{"app": app}}); err != nil {
		t.Fatal(err)
	}
	k.options.KubeRun = usageClusterFixture(k, false)
	result, err := k.Usage(context.Background(), "app")
	// Missing optional metrics must retain desired settings and explicit unavailability.
	if err != nil || result.Available || result.Reason == "" || result.Limits.CPU != "0.5" {
		t.Fatal(result, err)
	}
	k.options.KubeRun = usageClusterFixture(k, true)
	_, err = k.Usage(context.Background(), "app")
	// Foreign ownership must fail before any metrics request is admitted.
	if err == nil {
		t.Fatal("foreign namespace metrics admitted")
	}
}

// usageClusterFixture supplies owned live records while making the optional metrics API unavailable.
func usageClusterFixture(k *K3s, foreign bool) func(context.Context, []string, []byte) ([]byte, error) {
	// Execute only read-only fixture lookups and deterministic unavailable-metrics responses.
	return func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		joined := strings.Join(args, " ")
		// The optional metrics endpoint may be missing on a valid application cluster.
		if strings.Contains(joined, "--raw=") {
			return nil, errors.New("metrics not installed")
		}
		object := map[string]any{"metadata": k.metadata("app", "current"), "status": map[string]any{"readyReplicas": 1}}
		// Foreign namespaces use another complete owner identity.
		if foreign {
			object["metadata"] = map[string]any{"labels": map[string]any{"io.hostlier.owner": "foreign"}}
		}
		// Pod queries return a namespaced list with a current-version owned pod.
		if strings.Contains(joined, "get pods") {
			pod := map[string]any{"metadata": map[string]any{"name": "pod", "labels": map[string]string{"io.hostlier.managed": "true", "io.hostlier.owner": k.base.owner, "io.hostlier.app": "app", "io.hostlier.version": "current"}}}
			return json.Marshal(map[string]any{"items": []any{pod}})
		}
		return json.Marshal(object)
	}
}
