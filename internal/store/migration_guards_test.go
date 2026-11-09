package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSQLiteRejectsFutureSchema prevents silently interpreting a newer persistence format.
func TestSQLiteRejectsFutureSchema(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{}
	s, err := Open(dir, &state)
	// Stop the check when its prerequisite operation fails.
	if err != nil {
		t.Fatal(err)
	}
	// Preserve a valid key check while changing only the incompatible schema field.
	if _, err = s.db.Exec("UPDATE controller_snapshot SET schema_version=2"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Reopening must report the unsupported version explicitly.
	if reopened, err := Open(dir, &state); err == nil {
		reopened.Close()
		t.Fatal("future schema accepted")
		// Stop the check when its prerequisite operation fails.
	} else if !strings.Contains(err.Error(), "schema version 2") {
		t.Fatal(err)
	}
}

// TestLegacyMigrationRejectsSymlink prevents importing redirected state documents.
func TestLegacyMigrationRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "state.json")
	// Stop the check when its prerequisite operation fails.
	if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Stop the check when its prerequisite operation fails.
	if err := os.Symlink(target, filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	// The existing legacy path must be rejected even when its target contains valid JSON.
	state := map[string]any{}
	// Stop the check when its prerequisite operation fails.
	if opened, err := Open(dir, &state); err == nil {
		opened.Close()
		t.Fatal("legacy symlink accepted")
	}
}

// TestLegacyWorkflowResultRequiresOriginalKey checks the separately encrypted result field.
func TestLegacyWorkflowResultRequiresOriginalKey(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{}
	s, err := Open(dir, &state)
	// Stop the check when its prerequisite operation fails.
	if err != nil {
		t.Fatal(err)
	}
	// Produce a valid encrypted result under the original key.
	sealed, err := s.Encrypt("private output")
	// Stop the check when its prerequisite operation fails.
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Force migration with only workflowRecords encrypted, so other secret checks cannot mask it.
	if err = os.Remove(filepath.Join(dir, "state.db")); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"workflowRecords": map[string]any{"step": map[string]string{"result": sealed}}})
	// Stop the check when its prerequisite operation fails.
	if err != nil {
		t.Fatal(err)
	}
	// Write the legacy snapshot before substituting the wrong key.
	if err = os.WriteFile(filepath.Join(dir, "state.json"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	// Stop the check when its prerequisite operation fails.
	if err = os.WriteFile(filepath.Join(dir, "secret.key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	// A replacement key must not authenticate migration merely because workflowData is absent.
	if reopened, err := Open(dir, &state); err == nil {
		reopened.Close()
		t.Fatal("workflow result with wrong key accepted")
		// Stop the check when its prerequisite operation fails.
	} else if !strings.Contains(err.Error(), "workflow result authentication") {
		t.Fatal(err)
	}
}
