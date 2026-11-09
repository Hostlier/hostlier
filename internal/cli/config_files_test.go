package cli

import (
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigSnapshotConfinesSourceReads covers normal snapshots and source paths outside the project.
func TestConfigSnapshotConfinesSourceReads(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("private"), 0600)
	os.WriteFile(filepath.Join(root, "settings"), []byte("revision: one\n"), 0600)
	data, err := readConfigSource(root, model.ConfigFile{File: "settings"})
	// A successful snapshot preserves exact bytes for later immutable approval.
	if err != nil || data != "revision: one\n" {
		t.Fatalf("snapshot %q %v", data, err)
	}
	os.Symlink(outside, filepath.Join(root, "escaped"))
	// Symlinks, alternate sources, devices and oversized files cannot cross the upload boundary.
	for _, file := range []model.ConfigFile{{File: "escaped"}, {File: outside}, {File: "settings", Secret: "hidden"}} {
		// Every rejected input must fail before reading any escaped bytes.
		if _, err = readConfigSource(root, file); err == nil {
			t.Fatal("unsafe source accepted")
		}
	}
	os.WriteFile(filepath.Join(root, "oversized"), []byte(strings.Repeat("x", 65537)), 0600)
	// Files growing beyond the per-file bound are rejected too.
	if _, err = readConfigSource(root, model.ConfigFile{File: "oversized"}); err == nil {
		t.Fatal("oversized file accepted")
	}
}
