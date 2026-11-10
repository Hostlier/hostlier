package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestControllerUpdateBackupRetainsOnlyReceipts prevents pending executable stages
// from expanding metadata backups or silently transferring root execution payloads.
func TestControllerUpdateBackupRetainsOnlyReceipts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	state := map[string]any{"version": 2, "controllerUpdateId": "job"}
	s, err := Open(dir, &state)
	// A real writer snapshot must exist before exporting the maintenance metadata.
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	createUpdateBackupFixture(t, dir)
	bundle := filepath.Join(root, "metadata.tar")
	// Use real checksummed export and archive validation rather than testing only the filter.
	if err := s.Export(bundle, "", filepath.Join(root, "settings"), filepath.Join(root, "caddy")); err != nil {
		t.Fatal(err)
	}
	files, err := readBundle(bundle)
	// Validated receipts must survive without either executable or linked transient data.
	if err != nil {
		t.Fatal(err)
	}
	// Inspect all staged payload entries to prove the export boundary is exact.
	for _, name := range []string{"runner", "collector-layer"} {
		// No excluded executable stage member may appear in portable metadata.
		if _, exists := files["controller-updates/job/"+name]; exists {
			t.Fatal("staged update payload exported", name)
		}
	}
	// Retaining the original receipt lets restored controllers report interrupted jobs safely.
	if len(files["controller-updates/job/record.json"]) == 0 {
		t.Fatal("maintenance receipt lost")
	}
}

// createUpdateBackupFixture contrasts a retained receipt with disposable root execution payloads.
func createUpdateBackupFixture(t *testing.T, dir string) {
	t.Helper()
	stage := filepath.Join(dir, "controller-updates", "job")
	// Fixture data includes root-executed bytes that must not become backup metadata.
	if err := os.MkdirAll(stage, 0700); err != nil {
		t.Fatal(err)
	}
	// The root-only receipt remains available for status and interruption diagnosis.
	if err := os.WriteFile(filepath.Join(stage, "record.json"), []byte(`{"job":{"status":"queued"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Staging payloads are reproducible release material, not controller state.
	if err := os.WriteFile(filepath.Join(stage, "runner"), []byte("executable"), 0700); err != nil {
		t.Fatal(err)
	}
	// Export must not follow or validate excluded transient payload links.
	if err := os.Symlink("/etc/passwd", filepath.Join(stage, "collector-layer")); err != nil {
		t.Fatal(err)
	}
}
