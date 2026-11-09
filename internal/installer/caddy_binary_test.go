package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func caddyArchiveFixture(t *testing.T, duplicate, symlink bool) []byte {
	t.Helper()
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	archive := tar.NewWriter(compressed)
	entries := []tar.Header{{Name: "README.md", Typeflag: tar.TypeReg, Size: 4}, {Name: "caddy", Typeflag: tar.TypeReg, Size: 6}}
	if symlink {
		entries[1] = tar.Header{Name: "caddy", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}
	}
	if duplicate {
		entries = append(entries, entries[1])
	}
	for _, entry := range entries {
		if err := archive.WriteHeader(&entry); err != nil {
			t.Fatal(err)
		}
		if entry.Size > 0 {
			payload := []byte("read")
			if entry.Name == "caddy" {
				payload = []byte("binary")
			}
			if _, err := archive.Write(payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestCaddyArchiveRejectsForgedExecutableEntries(t *testing.T) {
	for _, scenario := range []struct {
		name               string
		duplicate, symlink bool
		valid              bool
	}{{name: "regular", valid: true}, {name: "duplicate", duplicate: true}, {name: "symlink", symlink: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "hostlier-caddy")
			err := saveCaddyArchive(caddyArchiveFixture(t, scenario.duplicate, scenario.symlink), target)
			if (err == nil) != scenario.valid {
				t.Fatalf("archive result: %v", err)
			}
			data, readErr := os.ReadFile(target)
			if scenario.valid && (readErr != nil || string(data) != "binary") {
				t.Fatalf("wrong executable: %q %v", data, readErr)
			}
			if !scenario.valid && !os.IsNotExist(readErr) {
				t.Fatalf("unsafe executable published: %v", readErr)
			}
		})
	}
}
