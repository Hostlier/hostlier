package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"hostlier/internal/model"
)

// testEmbedded owns the same real private broker used by the controller.
func testEmbedded(t *testing.T, directory string) *Embedded {
	t.Helper()
	bus, err := NewEmbedded(context.Background(), directory)
	// A broker fixture must fail before registering assertions if initialization is incomplete.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bus.Close) // Worker and broker cleanup is idempotent across explicit restart checks.
	return bus
}

// testStatusPayload constructs public telemetry with a stable deduplication identity.
func testStatusPayload(id string) string {
	value, _ := json.Marshal(model.StatusSnapshot{ID: id, Hostname: "controller.example.com", ObservedAt: time.Now().UTC(), Applications: []model.ApplicationHealth{{Name: "sample", Available: true, Running: true}}, Operations: []model.OperationHealth{}})
	return string(value)
}

// awaitEmbeddedSnapshot waits for consumer-visible state, never merely publisher queue admission.
func awaitEmbeddedSnapshot(t *testing.T, bus Bus, id string) string {
	t.Helper()
	updates, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Reading after subscribing prevents a notification race at first durable delivery.
	for {
		value := bus.Latest()
		var snapshot model.StatusSnapshot
		// Cache visibility requires the acknowledged stream consumer to process the matching capture.
		if json.Unmarshal([]byte(value), &snapshot) == nil && snapshot.ID == id {
			return value
		}
		// A broken broker or acknowledgement path must fail without hanging the test suite.
		select {
		// A newer replacement is now available in the immutable cache.
		case <-updates:
		// A timeout indicates capture never traversed the real JetStream stream.
		case <-ctx.Done():
			t.Fatalf("snapshot %s not delivered; latest=%s", id, value)
		}
	}
}

// TestEmbeddedJetStreamPrivateRecovery verifies no listeners, acknowledged publication and retained restart state.
func TestEmbeddedJetStreamPrivateRecovery(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "observations")
	bus := testEmbedded(t, directory)
	// In-process connections must not accidentally expose client or monitoring listeners.
	if bus.server.Addr() != nil || bus.server.MonitorAddr() != nil {
		t.Fatal("embedded broker exposed a network listener")
	}
	info, err := os.Stat(directory)
	// Sanitized telemetry remains owner-only operational data on disk.
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("telemetry storage is not private", err)
	}
	bus.Publish(testStatusPayload("first:1"))
	awaitEmbeddedSnapshot(t, bus, "first:1")
	awaitConsumerAck(t, bus)
	bus.Close()
	reopened := testEmbedded(t, directory)
	awaitEmbeddedSnapshot(t, reopened, "first:1")
	reopened.Publish(testStatusPayload("second:1"))
	awaitEmbeddedSnapshot(t, reopened, "second:1")
	awaitConsumerAck(t, reopened)
}

// awaitConsumerAck checks server-confirmed acknowledgement rather than assuming cache publication implies it.
func awaitConsumerAck(t *testing.T, bus *Embedded) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// DoubleAck happens after cache fanout, so inspect eventual server-side delivery state.
	for ctx.Err() == nil {
		info, err := bus.consumer.Info(ctx)
		// The durable consumer must advance its ack floor past the delivered snapshot.
		if err == nil && info.NumAckPending == 0 && info.AckFloor.Stream > 0 {
			return
		}
		observationPause(ctx, 10*time.Millisecond)
	}
	t.Fatal("JetStream consumer never confirmed acknowledgement")
}

// TestEmbeddedJetStreamRetentionAndDeduplication exercises real stream limits and lost-ack retry identity.
func TestEmbeddedJetStreamRetentionAndDeduplication(t *testing.T) {
	bus := testEmbedded(t, filepath.Join(t.TempDir(), "observations"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Direct acknowledged writes establish retention independently of capture coalescing.
	for i := 0; i < maxRetainedMessages+5; i++ {
		id := fmt.Sprintf("retention:%d", i)
		_, err := bus.js.Publish(ctx, statusSubject, []byte(testStatusPayload(id)), jetstream.WithMsgID(id))
		// A rejected message invalidates the test's expectation about the retained suffix.
		if err != nil {
			t.Fatal(err)
		}
	}
	lastID := fmt.Sprintf("retention:%d", maxRetainedMessages+4)
	ack, err := bus.js.Publish(ctx, statusSubject, []byte(testStatusPayload(lastID)), jetstream.WithMsgID(lastID))
	// An ambiguous publisher acknowledgement can be retried without adding another stored snapshot.
	if err != nil || !ack.Duplicate {
		t.Fatal("snapshot identity did not deduplicate", err)
	}
	info, err := bus.stream.Info(ctx)
	// Stream limits enforce finite message count, byte budget and retention age on the real server.
	if err != nil {
		t.Fatal(err)
	}
	assertRetentionLimits(t, info)
	awaitEmbeddedSnapshot(t, bus, lastID)
	awaitConsumerAck(t, bus)
}

// assertRetentionLimits checks the bounded production configuration alongside actual count eviction.
func assertRetentionLimits(t *testing.T, info *jetstream.StreamInfo) {
	t.Helper()
	// Server state must have discarded the oldest messages after the count budget filled.
	if info.State.Msgs != maxRetainedMessages || info.State.Bytes > maxRetainedBytes {
		t.Fatalf("retention exceeded: %+v", info.State)
	}
	// Byte and age limits protect installations even when the snapshot payload sizes vary.
	if info.Config.MaxBytes != maxRetainedBytes || info.Config.MaxAge != retentionAge || info.Config.MaxConsumers != 1 {
		t.Fatal("unbounded stream configuration")
	}
}

// TestEmbeddedJetStreamSanitizesBeforeStorage verifies private additions cannot leak into retained telemetry.
func TestEmbeddedJetStreamSanitizesBeforeStorage(t *testing.T) {
	bus := testEmbedded(t, filepath.Join(t.TempDir(), "observations"))
	payload := strings.TrimSuffix(testStatusPayload("sanitized:1"), "}") + `,"credentials":"PRIVATE_BROKER_VALUE","environment":{"TOKEN":"PRIVATE_BROKER_VALUE"}}`
	bus.Publish(payload)
	value := awaitEmbeddedSnapshot(t, bus, "sanitized:1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	retained, err := bus.stream.GetLastMsgForSubject(ctx, statusSubject)
	// Both the subscriber cache and file-backed stream contain only the public projection.
	if err != nil || strings.Contains(value, "PRIVATE_BROKER_VALUE") || strings.Contains(string(retained.Data), "PRIVATE_BROKER_VALUE") {
		t.Fatal("private fields reached JetStream", err)
	}
	bus.Publish(`{"id":"invalid","applications":"bad schema"}`)
	bus.Publish(strings.Repeat("x", maxSnapshotBytes+1))
	// Invalid data cannot even replace the bounded publisher queue's last valid capture.
	if strings.Contains(bus.pending.Latest(), "invalid") {
		t.Fatal("invalid snapshot entered publisher queue")
	}
}

// TestEmbeddedJetStreamOutageDoesNotBlockCapture proves a stalled broker cannot become capture backpressure.
func TestEmbeddedJetStreamOutageDoesNotBlockCapture(t *testing.T) {
	bus := testEmbedded(t, filepath.Join(t.TempDir(), "observations"))
	bus.Publish(testStatusPayload("healthy:1"))
	awaitEmbeddedSnapshot(t, bus, "healthy:1")
	bus.server.Shutdown()
	bus.server.WaitForShutdown()
	started := time.Now()
	// A broker outage must coalesce captures rather than wait on per-capture publication timeouts.
	for i := 0; i < 100; i++ {
		bus.Publish(testStatusPayload(fmt.Sprintf("outage:%d", i)))
	}
	// One publish timeout alone is a second; the full queue admission loop must remain independent.
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("capture blocked on broker outage")
	}
	var pending model.StatusSnapshot
	json.Unmarshal([]byte(bus.pending.Latest()), &pending)
	// Only the latest replacement is retained, and unpublished state never bypasses JetStream.
	if pending.ID != "outage:99" || !strings.Contains(bus.Latest(), "healthy:1") {
		t.Fatal("outage lost coalescing or bypassed broker acknowledgement")
	}
	started = time.Now()
	bus.Close()
	// Closing interrupts outstanding requests and reconnects rather than waiting for retry exhaustion.
	if time.Since(started) > time.Second {
		t.Fatal("outage delayed broker shutdown")
	}
}

// TestEmbeddedJetStreamRejectsRedirectedStorage keeps the private broker under controller ownership.
func TestEmbeddedJetStreamRejectsRedirectedStorage(t *testing.T) {
	root := t.TempDir()
	redirect := filepath.Join(root, "observations")
	// A redirected telemetry path must never be followed during embedded startup.
	if err := os.Symlink(t.TempDir(), redirect); err != nil {
		t.Fatal(err)
	}
	bus, err := NewEmbedded(context.Background(), redirect)
	// Failed initialization must not return a live broker or leave workers behind.
	if err == nil {
		bus.Close()
		t.Fatal("symlink storage was accepted")
	}
}

// TestEmbeddedCancelledInitializationClosesBroker exercises cleanup after a server starts but setup cannot finish.
func TestEmbeddedCancelledInitializationClosesBroker(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "observations")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bus, err := NewEmbedded(ctx, directory)
	// A canceled owner cannot receive an initialized observation service.
	if err == nil {
		bus.Close()
		t.Fatal("canceled initialization succeeded")
	}
	reopened := testEmbedded(t, directory)
	reopened.Publish(testStatusPayload("recovered:1"))
	awaitEmbeddedSnapshot(t, reopened, "recovered:1")
}
