package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// resourceObservationRuntime exposes numeric measurements alongside a deliberately private provider error.
type resourceObservationRuntime struct {
	recordingRuntime
	usage model.Usage
	err   error
}

// Usage returns fixture counters without runtime I/O or persistent state.
func (r *resourceObservationRuntime) Usage(context.Context, string) (model.Usage, error) {
	return r.usage, r.err
}

// TestSharedUsageReasonsAreSafe preserves partial memory measurements and explains missing CPU without publishing provider text.
func TestSharedUsageReasonsAreSafe(t *testing.T) {
	memory := int64(2097152)
	r := &resourceObservationRuntime{usage: model.Usage{MemoryBytes: &memory, Reason: "PRIVATE_PROVIDER_DETAIL"}}
	c := &Controller{opts: Options{Runtime: r}}
	app := &model.ApplicationHealth{Name: "app"}
	c.observeUsage(context.Background(), app)
	// First-sample memory must survive the shared DTO even while CPU is warming up.
	if app.Usage == nil || app.Usage.MemoryBytes == nil || app.Usage.Reason != "CPU sampling is warming up; memory is available" {
		t.Fatalf("partial measurement: %+v", app.Usage)
	}
	data, _ := json.Marshal(app)
	// Provider detail must never be serialized into the shared public observation.
	if strings.Contains(string(data), "PRIVATE_PROVIDER_DETAIL") {
		t.Fatal("private provider text reached observation transport")
	} // Arbitrary provider diagnostics remain outside the browser-visible stream.
	r.err = errors.New("PRIVATE_PROVIDER_ERROR")
	c.observeUsage(context.Background(), app)
	// A failed probe must retain an explicit safe reason rather than silently dropping all measurement metadata.
	if app.Usage.Reason != "Resource measurements unavailable" || app.Usage.MemoryBytes != nil {
		t.Fatalf("failed measurement: %+v", app.Usage)
	}
	r.err = nil
	r.usage = model.Usage{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.observeUsage(ctx, app)
	// Deadline/cancellation explains missing counters while preserving successful process health separately.
	if app.Usage.Reason != "Resource measurement timed out" {
		t.Fatal(app.Usage.Reason)
	}
}
