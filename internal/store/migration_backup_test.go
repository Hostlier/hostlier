package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrationVolumeDataIsNotControllerMetadata prevents recursive backups and
// keeps arbitrary application filesystem entries out of metadata-only recovery.
func TestMigrationVolumeDataIsNotControllerMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	state := map[string]any{"version": 2}
	// Check the observed result against the fixture's migration invariant.
	s, err := Open(dir, &state)
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data := filepath.Join(dir, "migration-data", "id", "volume")
	// Check the observed result against the fixture's migration invariant.
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	// Check the observed result against the fixture's migration invariant.
	if err := os.Symlink("/etc/passwd", filepath.Join(data, "app-link")); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "metadata.tar")
	// Check the observed result against the fixture's migration invariant.
	if err := s.Export(bundle, "", filepath.Join(root, "settings"), filepath.Join(root, "caddy")); err != nil {
		t.Fatal(err)
	}
	// Check the observed result against the fixture's migration invariant.
	files, err := readBundle(bundle)
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
	// Check the observed result against the fixture's migration invariant.
	if _, ok := files["migration-data/id/volume/app-link"]; ok {
		t.Fatal("application volume included in metadata")
	}
	// Check the observed result against the fixture's migration invariant.
	if len(files["secret.key"]) != 32 {
		t.Fatal("original encryption key was not backed up")
	}
}
