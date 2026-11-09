package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRecoveryExcludesJetStreamTelemetry keeps reconstructible broker state out of authoritative backups.
func TestRecoveryExcludesJetStreamTelemetry(t *testing.T) {
	directory := t.TempDir()
	var state map[string]any
	store, err := Open(directory, &state)
	// A real controller store establishes the ordinary exclusive recovery boundary.
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	writeExcludedRecoveryFiles(t, directory)
	bundle := filepath.Join(t.TempDir(), "controller.tar")
	// Export uses the real snapshot and archive path, not a test-only filter invocation.
	if err = Export(directory, bundle, ""); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	// Restoring authoritative metadata must succeed without the embedded broker's retained files.
	if err = Restore(bundle, restored); err != nil {
		t.Fatal(err)
	}
	// Cached observations must be freshly collected after recovery, never imported as readiness evidence.
	if _, err = os.Stat(filepath.Join(restored, "observations")); !os.IsNotExist(err) {
		t.Fatal("recovery included JetStream telemetry", err)
	}
	// Large image layers must be supplied by the release again after recovery.
	if _, err = os.Stat(filepath.Join(restored, "collector-image")); !os.IsNotExist(err) {
		t.Fatal("recovery included image cache", err)
	}
	// The durable state snapshot remains part of the recovery bundle.
	if _, err = os.Stat(filepath.Join(restored, "state.db")); err != nil {
		t.Fatal("recovery lost authoritative state", err)
	}
}

// writeExcludedRecoveryFiles populates disposable image and telemetry data for the real recovery walk.
func writeExcludedRecoveryFiles(t *testing.T, directory string) {
	t.Helper()
	image := filepath.Join(directory, "collector-image", "blobs", "sha256")
	// Release image layers are reproducible assets, not controller authority.
	if err := os.MkdirAll(image, 0700); err != nil {
		t.Fatal(err)
	}
	// Include a fake layer to exercise the production recovery walk.
	if err := os.WriteFile(filepath.Join(image, "layer"), []byte("collector image"), 0600); err != nil {
		t.Fatal(err)
	}
	telemetry := filepath.Join(directory, "observations", "jetstream", "$G", "streams", "HOSTLIER_STATUS")
	// Nested server storage is disposable telemetry rather than a portable controller database.
	if err := os.MkdirAll(telemetry, 0700); err != nil {
		t.Fatal(err)
	}
	// A retained message would otherwise be collected by the recovery directory walk.
	if err := os.WriteFile(filepath.Join(telemetry, "capture.blk"), []byte("retained observation"), 0600); err != nil {
		t.Fatal(err)
	}
}
