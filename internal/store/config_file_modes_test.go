package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestManagedConfigRecoveryPreservesReadOnlyModes verifies readable container files behind private restored parents.
func TestManagedConfigRecoveryPreservesReadOnlyModes(t *testing.T) {
	source := t.TempDir()
	name := "config-files/operation/app/settings"
	path := filepath.Join(source, name)
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("revision-one"), 0444)
	files := map[string][]byte{name: []byte("revision-one"), "private-key": []byte("protected")}
	// Capture modes from the same source bytes included in the recovery bundle.
	if err := collectManagedConfigModes(source, files); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	// Restoration must preserve read-only mode while keeping credentials owner-only.
	if err := restoreFiles(dest, files); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.Stat(filepath.Join(dest, name))
	parent, _ := os.Stat(filepath.Join(dest, "config-files"))
	key, _ := os.Stat(filepath.Join(dest, "private-key"))
	// Parent privacy ensures world-readable container mount bits do not expose files to host users.
	if restored.Mode().Perm() != 0444 || parent.Mode().Perm() != 0700 || key.Mode().Perm() != 0600 {
		t.Fatal("recovery changed file access boundaries")
	}
}

// TestManagedConfigModeIndexCannotChangeOtherControllerFiles prevents mode metadata from broadening executable/key access.
func TestManagedConfigModeIndexCannotChangeOtherControllerFiles(t *testing.T) {
	files := map[string][]byte{"secret.key": []byte("protected"), managedModesFile: []byte(`{"secret.key":292}`)}
	// Only actual managed file paths may receive a read-only mount mode.
	if _, err := restoredManagedConfigModes(files); err == nil {
		t.Fatal("private key mode override accepted")
	}
}
