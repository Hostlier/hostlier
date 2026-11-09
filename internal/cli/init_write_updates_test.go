package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestInitSetupPreservesConfigAndMode checks replacement alongside an exclusively created deployment file.
func TestInitSetupPreservesConfigAndMode(t *testing.T) {
	original := `{"entry":"app.ts","custom":{"keep":true}}`
	updated := `{"entry":"app.ts","custom":{"keep":true},"route":"app.example.com"}`
	root := initFixture(t, map[string]string{"minion.json": original})
	path := filepath.Join(root, "minion.json")
	// User-authored permission policy survives atomic replacement.
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{path: updated, filepath.Join(root, "generated", ".hostlier"): "version: 2\n"}
	// New files and reviewed replacements form one successful setup transaction.
	if err := writeInitSetup(root, files, map[string]string{path: original}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	// Configuration mode must not be broadened or reset to a generic default.
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal(info, err)
	}
	tree := initTree(t, root)
	// The merged source and the ordinary scaffold are written exactly as reviewed.
	if tree[path] != updated || tree[filepath.Join(root, "generated", ".hostlier")] != "version: 2\n" || len(tree) != 2 {
		t.Fatal("setup outputs differ")
	}
}

// TestInitSetupConflictLeavesAllFilesUntouched preflights a later-sorted conflict before touching minion.json.
func TestInitSetupConflictLeavesAllFilesUntouched(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": "original", "z-existing": "user-owned"})
	path := filepath.Join(root, "minion.json")
	before := initTree(t, root)
	err := writeInitSetup(root, map[string]string{path: "updated", filepath.Join(root, "z-existing"): "replacement"}, map[string]string{path: "original"})
	// Conflict diagnostics do not leak source bytes, and no writes precede complete preflight.
	if err == nil || !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("conflict modified setup files", err)
	}
}

// TestInitSetupRejectsStaleOriginal detects a user edit made before transactional preflight.
func TestInitSetupRejectsStaleOriginal(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": "user-updated-private-value"})
	path := filepath.Join(root, "minion.json")
	before := initTree(t, root)
	err := writeInitSetup(root, map[string]string{path: "new"}, map[string]string{path: "old-private-value"})
	// Original values are neither accepted after an edit nor included in the diagnostic.
	if err == nil || strings.Contains(err.Error(), "private-value") || !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("stale original was not preserved", err)
	}
}

// TestInitSetupRejectsSymlinkTargetsAndParents keeps configuration edits within the selected app.
func TestInitSetupRejectsSymlinkTargetsAndParents(t *testing.T) {
	// Both a file link and an ancestor link must fail before any files are written.
	for _, parent := range []bool{false, true} {
		// Exercise the same containment rule for each redirecting filesystem shape.
		t.Run(map[bool]string{false: "file", true: "parent"}[parent], func(t *testing.T) {
			root := initFixture(t, map[string]string{})
			outside := initFixture(t, map[string]string{"minion.json": "outside"})
			path := filepath.Join(root, "minion.json")
			source, target := filepath.Join(outside, "minion.json"), path
			// Parent redirects are rejected even when their eventual file is regular.
			if parent {
				source, target = outside, filepath.Join(root, "nested")
				path = filepath.Join(target, "minion.json")
			}
			// Create an explicit redirecting fixture, never an external write target.
			if err := os.Symlink(source, target); err != nil {
				t.Fatal(err)
			}
			before := initTree(t, root)
			err := writeInitSetup(root, map[string]string{path: "updated"}, map[string]string{path: "outside"})
			// Symlinks and their targets must remain untouched after rejection.
			if err == nil || !reflect.DeepEqual(before, initTree(t, root)) {
				t.Fatal("symlink accepted", err)
			}
			contents, _ := os.ReadFile(filepath.Join(outside, "minion.json"))
			// The linked external config is never opened for writing.
			if string(contents) != "outside" {
				t.Fatal("external file changed")
			}
		})
	}
}

// TestInitSetupDetectsEditsAfterPlanning verifies the comparison immediately before replacement.
func TestInitSetupDetectsEditsAfterPlanning(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": "original"})
	path := filepath.Join(root, "minion.json")
	writes, err := planInitSetup(root, map[string]string{path: "generated"}, map[string]string{path: "original"})
	// Planning snapshots the existing inode as well as its contents.
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an editor changing the same inode after the setup prompt completed.
	if err = os.WriteFile(path, []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	directories := []string{}
	err = applyInitSetup(root, writes[0], &directories)
	defer os.Remove(writes[0].staged)
	data, _ := os.ReadFile(path)
	// Changed bytes must never be overwritten by the staged replacement.
	if err == nil || string(data) != "edited" {
		t.Fatal("concurrent edit lost", err)
	}
}

// TestInitSetupRollbackPreservesLaterEdits restores only outputs still owned by the failed transaction.
func TestInitSetupRollbackPreservesLaterEdits(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": "original"})
	path := filepath.Join(root, "minion.json")
	files := map[string]string{path: "generated", filepath.Join(root, "new"): "created"}
	writes, err := planInitSetup(root, files, map[string]string{path: "original"})
	// Use the real transaction stages to exercise rollback ownership checks.
	if err != nil {
		t.Fatal(err)
	}
	directories := []string{}
	// Both new and replacement files must be tracked by identity after application.
	for _, write := range writes {
		// Failed fixture writes cannot meaningfully exercise rollback ownership.
		if err = applyInitSetup(root, write, &directories); err != nil {
			t.Fatal(err)
		}
	}
	// A user's edit after creation is outside transaction ownership.
	if err = os.WriteFile(filepath.Join(root, "new"), []byte("user-edit"), 0600); err != nil {
		t.Fatal(err)
	}
	rollbackInitSetup(writes, directories)
	tree := initTree(t, root)
	// The untouched replacement rolls back while the edited new file survives.
	if tree[path] != "original" || tree[filepath.Join(root, "new")] != "user-edit" {
		t.Fatal("rollback lost authored data")
	}
}
