package cli

import (
	"context"
	"hostlier/internal/controller"
	"hostlier/internal/model"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// usageRuntimeFixture supplies measured values while retaining the ordinary deployment fixture behavior.
type usageRuntimeFixture struct {
	recordingRuntime
	observation model.Usage
	usageCalls  int
}

// Usage returns explicit fixture measurements and records whether authorization admitted observation.
func (r *usageRuntimeFixture) Usage(context.Context, string) (model.Usage, error) {
	r.usageCalls++
	return r.observation, nil
}

// TestUsageCLIReadOnlyAuthenticatedMetrics exercises real CLI dispatch and controller authorization without listeners.
func TestUsageCLIReadOnlyAuthenticatedMetrics(t *testing.T) {
	dir := project(t)
	runtime := &usageRuntimeFixture{}
	server, err := controller.New(controller.Options{StateDir: filepath.Join(dir, "state"), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: runtime})
	// The real controller maintains application existence and authorizes runtime observation.
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	// Supply the fixture's explicit interactive approval response.
	options := Options{CWD: dir, Home: filepath.Join(dir, "home"), Client: newWorkflowAPI(t, withFixtureHuman(t, server, server.LocalHandler())), PollInterval: time.Millisecond, Prompt: func(string, bool) (string, error) { return "yes", nil }}
	_, err = invoke([]string{"deploy"}, "", options)
	// An ordinary approved deployment establishes the fixture application before usage is queried.
	if err != nil {
		t.Fatal(err)
	}
	options.Prompt = nil
	denied := options
	denied.Client = workflowAPI{handler: server.Handler()}
	_, err = invoke([]string{"usage", "fixture"}, "", denied)
	// Unauthenticated remote management cannot read any workload metrics.
	if err == nil || runtime.usageCalls != 0 {
		t.Fatal("unauthenticated usage admitted", err)
	}
	assertUsageCLIOutput(t, runtime, options)
	// Observing metrics cannot deploy another workload or request interactive approval.
	if runtime.count() != 1 || runtime.usageCalls != 2 {
		t.Fatal("usage mutated workload state")
	}
}

// assertUsageCLIOutput verifies genuine numeric zero and explicit unavailable observations remain distinct.
func assertUsageCLIOutput(t *testing.T, runtime *usageRuntimeFixture, options Options) {
	t.Helper()
	zeroCPU, zeroMemory := float64(0), int64(0)
	runtime.observation = model.Usage{Available: true, Source: "fixture", CPUCores: &zeroCPU, MemoryBytes: &zeroMemory, DesiredReplicas: 1, ReadyReplicas: 1}
	output, err := invoke([]string{"usage", "fixture"}, "", options)
	// Actual idle measurements must serialize as numeric zero rather than disappear.
	if err != nil || !strings.Contains(output, `"cpuCores":0`) || !strings.Contains(output, `"memoryBytes":0`) {
		t.Fatal(output, err)
	}
	runtime.observation = model.Usage{Available: false, Source: "fixture", Reason: "Metrics unavailable", DesiredReplicas: 1}
	output, err = invoke([]string{"usage", "fixture"}, "", options)
	// Missing measurements must not become fabricated zero-valued consumption.
	if err != nil || !strings.Contains(output, `"available":false`) || strings.Contains(output, `"cpuCores"`) || strings.Contains(output, `"memoryBytes"`) {
		t.Fatal(output, err)
	}
}

// TestUsageCLIRejectsMissingApplication exercises argument admission without contacting management APIs.
func TestUsageCLIRejectsMissingApplication(t *testing.T) {
	_, err := runUsageWithoutClient()
	// Missing application identity must be an argument error rather than a guessed observation.
	if err == nil {
		t.Fatal("usage without application accepted")
	}
}

// runUsageWithoutClient invokes the read-only command without a target or available controller connection.
func runUsageWithoutClient() (string, error) {
	return invoke([]string{"usage"}, "", Options{CWD: "/tmp", Home: "/tmp/hostlier-usage-no-profile", Client: workflowAPI{handler: nil}})
}
