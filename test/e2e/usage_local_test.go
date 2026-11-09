package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
	"os"
	"testing"
	"time"
)

// TestDockerUsageObservedMetrics checks real CPU/memory measurements and applied daemon limits on an owned workload.
func TestDockerUsageObservedMetrics(t *testing.T) {
	// Explicit opt-in is required for disposable labeled Docker workloads.
	if os.Getenv("HOSTLIER_DOCKER_E2E") != "1" {
		t.Skip("set HOSTLIER_DOCKER_E2E=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	sum := sha256.Sum256([]byte(dir))
	owner := hex.EncodeToString(sum[:])[:16]
	socket := os.Getenv("HOSTLIER_DOCKER_SOCKET")
	// The fixture follows the production runtime's default socket placement.
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	docker := dockerCommand(t, ctx, socket)
	cleanupDockerOwner(t, socket, owner)
	admin, _, _ := startCaddyFixture(t, os.Getenv("HOSTLIER_CADDY_BINARY"), dir)
	adapter, err := hostruntime.New(hostruntime.Options{StateDir: dir, SocketPath: socket, CaddyAdmin: admin, SkipPublicHTTPSProbe: true, HealthTimeout: 5 * time.Second})
	// The production deployment path must create and own the application before metrics are observed.
	if err != nil {
		t.Fatal(err)
	}
	image := inspectDockerFixtureImage(t, ctx, adapter, docker, dir)
	spec := model.Project{Version: 1, Name: "usage", Image: image.Digest, Port: 80, Route: "usage.hostlier.test", Resources: &model.Limits{CPU: "0.5", Memory: "128Mi"}}
	app := scalingDeploy(t, ctx, adapter, spec, "usage-approved-fixture")
	id := app.RuntimeID
	// Controlled external drift proves the observer reads applied daemon limits independently of recorded intent.
	docker("update", "--cpus=0.25", "--memory=64m", id)
	docker("exec", "-d", id, "sh", "-c", "while :; do :; done")
	observer := adapter.(model.UsageRuntime)
	result, err := observer.Usage(ctx, "usage")
	assertLiveUsage(t, result, err)
	docker("stop", "-t", "1", id)
	result, err = observer.Usage(ctx, "usage")
	// A stopped workload is unavailable and must never produce fabricated zero measurements.
	if err != nil || result.Available || result.CPUCores != nil || result.MemoryBytes != nil || result.ReadyReplicas != 0 || result.Reason == "" {
		t.Fatal(result, err)
	}
}

// assertLiveUsage compares actual measurements, readiness and effective limits against separate desired settings.
func assertLiveUsage(t *testing.T, result model.Usage, err error) {
	t.Helper()
	// The busy workload must consume measurable CPU and memory from Docker's actual counters.
	if err != nil || !result.Available || result.CPUCores == nil || result.MemoryBytes == nil {
		t.Fatal(result, err)
	}
	// Genuine consumption is distinct from desired or enforced capacity.
	if *result.CPUCores <= 0 || *result.MemoryBytes <= 0 || result.CPUCapacityCores != 0.25 || result.MemoryLimitBytes != 67108864 {
		t.Fatal(result)
	}
	assertUsageIntent(t, result)
}

// assertUsageIntent checks recorded settings and ready counts independently of measurement availability.
func assertUsageIntent(t *testing.T, result model.Usage) {
	t.Helper()
	// Recorded intent remains visible alongside the independently observed effective allocation.
	if result.Limits.CPU != "0.5" || result.Limits.Memory != "128Mi" || result.DesiredReplicas != 1 || result.ReadyReplicas != 1 {
		t.Fatal(result)
	}
}
