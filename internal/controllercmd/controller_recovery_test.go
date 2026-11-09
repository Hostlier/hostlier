package controllercmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"hostlier/internal/store"
)

// TestControllerRecoveryCLI exercises offline backup and restore through the packaged command dispatcher.
func TestControllerRecoveryCLI(t *testing.T) {
	source := t.TempDir()
	state := map[string]any{"eventSequence": float64(63)}
	writer, err := store.Open(source, &state)
	// The fixture must commit initial controller state before invoking the CLI.
	if err != nil {
		t.Fatal(err)
	}
	// Persist an event sequence representative of a controller with prior operations.
	if err = writer.Save(map[string]any{"eventSequence": float64(63)}); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	config := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(config, []byte("version: 1\n"), 0600)
	bundle := filepath.Join(t.TempDir(), "backup.tar")
	output := &bytes.Buffer{}
	// Backup runs without any remote controller or authentication profile.
	if err = Run(context.Background(), []string{"controller", "backup", "--state-dir", source, "--output", bundle, "--config", config}, bytes.NewReader(nil), output, output); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	// Restore requires the explicit assertion that the original controller is stopped.
	if err = Run(context.Background(), []string{"controller", "restore", "--state-dir", destination, "--input", bundle, "--source-stopped"}, bytes.NewReader(nil), output, output); err != nil {
		t.Fatal(err)
	}
	recovered := map[string]any{}
	writer, err = store.Open(destination, &recovered)
	// The recovered SQLite store must be independently reopenable.
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	// Preserve event cursors across the command-level recovery flow.
	if recovered["eventSequence"] != float64(63) {
		t.Fatal(recovered)
	}
}
