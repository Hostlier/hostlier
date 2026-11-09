package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"hostlier/internal/model"
)

// usageTransport isolates diagnostic HTTP calls without requiring a daemon or network listener.
type usageTransport func(*http.Request) (*http.Response, error)

// RoundTrip implements the native Docker client's injected transport contract.
func (f usageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// usageResponse encodes exactly the daemon fields under test.
func usageResponse(value any) (*http.Response, error) {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
}

// cpuSample creates immutable cumulative counters for a real delta calculation, never a synthetic utilization value.
func cpuSample(total, system uint64) dockerStats {
	var stats dockerStats
	stats.CPU.CPUUsage.Total = total
	stats.CPU.System = system
	stats.CPU.Online = 4
	return stats
}

// TestDockerOneShotUsageFitsCaptureBudget prevents Docker's two-cycle wait from consuming the entire shared probe deadline.
func TestDockerOneShotUsageFitsCaptureBudget(t *testing.T) {
	sequence := 0
	// Every fixture request uses the same native client path as a real daemon query.
	r := &Runtime{docker: &http.Client{Transport: usageTransport(func(request *http.Request) (*http.Response, error) {
		// Stats calls must be single-shot; the old default blocks until its two-sample deadline expires.
		if strings.HasSuffix(request.URL.Path, "/stats") {
			// A missing query flag deterministically simulates the Docker delay without slow wall-clock sleeps.
			if request.URL.Query().Get("stream") != "false" || request.URL.Query().Get("one-shot") != "true" {
				<-request.Context().Done()
				return nil, request.Context().Err()
			}
			sequence++
			stats := cpuSample(100, uint64(1000+sequence*100))
			stats.Read = time.Unix(100+int64(sequence), 0).UTC().Format(time.RFC3339Nano)
			stats.Memory.Usage = 2097152
			return usageResponse(stats)
		}
		return usageResponse(map[string]any{"HostConfig": map[string]any{"NanoCpus": 500000000, "Memory": 67108864}})
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	first, err := r.dockerUsage(ctx, "owned-container", model.Usage{})
	assertWarmupMemory(t, first, err)
	second, err := r.dockerUsage(ctx, "owned-container", model.Usage{})
	assertIdleMeasurement(t, second, err)
}

// TestDockerCounterBaselinesRejectResetsAndOutOfOrderSamples protects ownership and counter-delta correctness across concurrent reads.
func TestDockerCounterBaselinesRejectResetsAndOutOfOrderSamples(t *testing.T) {
	r := &Runtime{}
	cases := []struct {
		name, id      string
		stamp         int64
		total, system uint64
		available     bool
		cores         float64
	}{
		{"cold", "a", 1, 100, 1000, false, 0}, {"delta", "a", 2, 150, 1200, true, 1}, {"duplicate", "a", 2, 150, 1200, true, 1}, {"out-of-order", "a", 1, 100, 1000, false, 0}, {"inconsistent-timestamp", "a", 2, 999, 9999, false, 0}, {"reset", "a", 3, 2, 50, false, 0}, {"idle", "a", 4, 2, 250, true, 0}, {"other-container", "b", 5, 200, 2000, false, 0},
	}
	// Each sequential observation exercises a different baseline transition without resetting the runtime cache.
	for _, item := range cases {
		actual, available := r.sampleDockerCPU(item.id, cpuSample(item.total, item.system), time.Unix(item.stamp, 0))
		// Only a forward, same-container delta or identical already-calculated sample can report CPU usage.
		if available != item.available || actual != item.cores {
			t.Fatalf("%s: got %v/%v", item.name, actual, available)
		}
	}
}

// TestDockerCounterCacheIsBoundedAndConcurrent keeps retired containers from accumulating observation state indefinitely.
func TestDockerCounterCacheIsBoundedAndConcurrent(t *testing.T) {
	r := &Runtime{}
	// More retired identities than capacity must evict the oldest counter baseline.
	for index := range 257 {
		r.sampleDockerCPU(string(rune(index+1)), cpuSample(1, 1000), time.Unix(int64(index+1), 0))
	}
	// The oldest identity must be evicted while the fixed maximum remains intact.
	if len(r.usageSamples) != 256 {
		t.Fatal("counter cache exceeded its bound")
	}
	// The least recently observed retired identity must no longer consume cache capacity.
	if _, exists := r.usageSamples[string(rune(1))]; exists {
		t.Fatal("oldest baseline retained")
	} // Recent workload counters take precedence over retired ones.
	var group sync.WaitGroup
	// Concurrent readers of one container can arrive out of order without racing or replacing the newest baseline.
	for index := range 32 {
		// Each concurrent consumer keeps its own timestamp while sharing the protected baseline map.
		group.Go(func() {
			r.sampleDockerCPU("shared", cpuSample(uint64(index), uint64(1000+index)), time.Unix(int64(1000+index), 0))
		})
	}
	group.Wait()
	// The accepted baseline always corresponds to the latest sample regardless of arrival order.
	if !r.usageSamples["shared"].observed.Equal(time.Unix(1031, 0)) {
		t.Fatal("concurrent reads regressed the CPU baseline")
	}
}

// assertWarmupMemory verifies that a cold CPU baseline does not hide the independently measured memory sample.
func assertWarmupMemory(t *testing.T, first model.Usage, err error) {
	t.Helper()
	// Cold CPU counters must not be presented as a measured utilization value.
	if err != nil || first.Available || first.CPUCores != nil || first.MemoryBytes == nil || first.Reason == "" {
		t.Fatalf("first sample: %+v %v", first, err)
	}
}

// assertIdleMeasurement verifies that a measured zero CPU delta remains available alongside actual daemon limits.
func assertIdleMeasurement(t *testing.T, second model.Usage, err error) {
	t.Helper()
	// Complete samples carry both counters while preserving an idle process as a genuine zero measurement.
	if err != nil || !second.Available || second.CPUCores == nil || *second.CPUCores != 0 || *second.MemoryBytes != 2097152 || second.CPUCapacityCores != 0.5 {
		t.Fatalf("second sample: %+v %v", second, err)
	}
}
