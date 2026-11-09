package store

import (
	"encoding/json"
	"hostlier/internal/model"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLegacyMigrationAndRecovery preserves ownership, event cursors and original encrypted bytes.
func TestLegacyMigrationAndRecovery(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{}
	s, err := Open(dir, &state)
	// Propagate failures before publishing recovery state.
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := s.Encrypt("original-private-value")
	// Propagate failures before publishing recovery state.
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.Remove(filepath.Join(dir, "state.db"))
	legacy := map[string]any{"workflowNamespace": "namespace-original", "eventSequence": float64(42), "workflowData": map[string]string{"plan-original": cipher}}
	data, _ := json.Marshal(legacy)
	os.WriteFile(filepath.Join(dir, "state.json"), data, 0600)
	s, err = Open(dir, &state)
	// Propagate failures before publishing recovery state.
	if err != nil {
		t.Fatal(err)
	}
	// Validate the precondition before accepting protected state.
	if state["eventSequence"] != float64(42) {
		t.Fatal(state)
	}
	s.Close()
	// Validate the precondition before accepting protected state.
	if _, err = os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal("migration removed source", err)
	}
	os.Mkdir(filepath.Join(dir, "packages"), 0700)
	os.WriteFile(filepath.Join(dir, "packages", "example"), []byte("binary-contents"), 0700)
	os.WriteFile(filepath.Join(dir, "adapter.json"), []byte("adapter-ownership"), 0600)
	bundle := filepath.Join(t.TempDir(), "controller.tar")
	// Propagate failures before publishing recovery state.
	if err = Export(dir, bundle, ""); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	// Propagate failures before publishing recovery state.
	if err = Restore(bundle, destination); err != nil {
		t.Fatal(err)
	}
	assertRecovered(t, destination, cipher)
	// Propagate failures before publishing recovery state.
	if err = Restore(bundle, destination); err == nil {
		t.Fatal("overwrote destination")
	}
}

// assertRecovered proves decryption, ownership, cursor and executable metadata survived.
func assertRecovered(t *testing.T, dir, cipher string) {
	t.Helper()
	state := map[string]any{}
	s, err := Open(dir, &state)
	// Propagate failures before publishing recovery state.
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	plain, err := s.Decrypt(cipher)
	// Propagate failures before publishing recovery state.
	if err != nil || plain != "original-private-value" {
		t.Fatal(plain, err)
	}
	// Validate the precondition before accepting protected state.
	if state["workflowNamespace"] != "namespace-original" || state["eventSequence"] != float64(42) {
		t.Fatal(state)
	}
	data, err := os.ReadFile(filepath.Join(dir, "packages", "example"))
	// Propagate failures before publishing recovery state.
	if err != nil || string(data) != "binary-contents" {
		t.Fatal(string(data), err)
	}
}

// TestRecoveryRefusesWriterCorruptionAndSymlinks verifies offline ownership and archive safety.
func TestRecoveryRefusesWriterCorruptionAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{}
	s, err := Open(dir, &state)
	// Propagate failures before publishing recovery state.
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "controller.tar")
	// Propagate failures before publishing recovery state.
	if err = Export(dir, bundle, ""); err == nil {
		t.Fatal("backup allowed existing writer")
	}
	s.Close()
	os.WriteFile(bundle, []byte("corrupt"), 0600)
	// Propagate failures before publishing recovery state.
	if err = Restore(bundle, filepath.Join(t.TempDir(), "target")); err == nil {
		t.Fatal("corruption accepted")
	}
	os.Remove(bundle)
	os.Symlink(filepath.Join(dir, "secret.key"), filepath.Join(dir, "bad-link"))
	// Propagate failures before publishing recovery state.
	if err = Export(dir, bundle, ""); err == nil {
		t.Fatal("symlink accepted")
	}
}

// TestMigrationWrongKeyRemainsIncomplete verifies failed authentication never creates committed migration state.
func TestMigrationWrongKeyRemainsIncomplete(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{}
	s, err := Open(dir, &state)
	// Propagate failures before publishing recovery state.
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := s.Encrypt("private")
	s.Close()
	os.Remove(filepath.Join(dir, "state.db"))
	data, _ := json.Marshal(map[string]any{"workflowData": map[string]string{"plan": sealed}})
	os.WriteFile(filepath.Join(dir, "state.json"), data, 0600)
	key, _ := os.ReadFile(filepath.Join(dir, "secret.key"))
	os.WriteFile(filepath.Join(dir, "secret.key"), make([]byte, 32), 0600)
	// Validate the precondition before accepting protected state.
	if bad, err := Open(dir, &state); err == nil {
		bad.Close()
		t.Fatal("wrong key accepted")
	}
	os.WriteFile(filepath.Join(dir, "secret.key"), key, 0600)
	s, err = Open(dir, &state)
	// Propagate failures before publishing recovery state.
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

// TestSQLiteCrashReopen commits before abrupt process exit and verifies reopening in another process.
func TestSQLiteCrashReopen(t *testing.T) {
	// The child deliberately skips Close to exercise OS lock release and SQLite recovery.
	if dir := os.Getenv("HOSTLIER_CRASH_STORE"); dir != "" {
		state := map[string]any{}
		s, err := Open(dir, &state)
		// The child must durably commit the cursor before simulating a crash.
		if err != nil {
			os.Exit(20)
		}
		// A successful Save includes workflow result and event cursor in one transaction.
		if s.Save(map[string]any{"eventSequence": float64(91), "workflowNamespace": "crash-owner"}) != nil {
			os.Exit(21)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestSQLiteCrashReopen$")
	command.Env = append(os.Environ(), "HOSTLIER_CRASH_STORE="+dir)
	// Report subprocess failures with diagnostic output.
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", output, err)
	}
	state := map[string]any{}
	s, err := Open(dir, &state)
	// OS ownership must be released even when the writer skipped Close.
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The committed ownership and event sequence survive the abrupt exit together.
	if state["eventSequence"] != float64(91) || state["workflowNamespace"] != "crash-owner" {
		t.Fatal(state)
	}
}

// TestSystemDirectoryAncestry terminates on Linux real directories and macOS standard aliases.
func TestSystemDirectoryAncestry(t *testing.T) {
	// These standard roots must never recurse forever when symlink resolution is unchanged.
	for _, path := range []string{"/", "/var", "/tmp"} {
		// Directory inspection must finish without creating or changing system files.
		if err := safeStateDirectory(path); err != nil {
			t.Fatal(path, err)
		}
		// Restore ancestry uses the same trusted-alias termination rule.
		if err := rejectSymlinkAncestors(path); err != nil {
			t.Fatal(path, err)
		}
	}
}

// TestRestoreOwnershipFenceRequiresOriginalOperationalPath prevents accidental resource namespace changes.
func TestRestoreOwnershipFenceRequiresOriginalOperationalPath(t *testing.T) {
	source := t.TempDir()
	state := map[string]any{}
	writer, err := Open(source, &state)
	// A real committed SQLite file is required for a valid recovery bundle.
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	bundle := filepath.Join(t.TempDir(), "controller.tar")
	// Export records the original exact absolute operational state directory.
	if err = Export(source, bundle, ""); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(t.TempDir(), "staged")
	// Restoration may be staged elsewhere for inspection before activation.
	if err = Restore(bundle, staging); err != nil {
		t.Fatal(err)
	}
	// A staged restore cannot silently activate a different Docker or database namespace.
	if err = model.ValidateRecoveryLocation(staging); err == nil {
		t.Fatal("different operational path allowed")
	}
	// Re-export cannot rebase original ownership to the staging directory.
	if err = Export(staging, filepath.Join(t.TempDir(), "rebased.tar"), ""); err == nil {
		t.Fatal("staged ownership silently rebased")
	}
	originalMoved := source + ".before-recovery"
	os.Rename(source, originalMoved)
	// Collect only safe regular controller files under the exclusive lock.
	t.Cleanup(func() { os.RemoveAll(originalMoved) })
	// A fresh host may restore at exactly the original path after fencing the source.
	if err = Restore(bundle, source); err != nil {
		t.Fatal(err)
	}
	// Every resource adapter now receives the same original namespace input.
	if err = model.ValidateRecoveryLocation(source); err != nil {
		t.Fatal(err)
	}
}
