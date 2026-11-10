package migration

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLinuxColdArchiveRoundTrip uses real GNU tar to verify sparse data and links.
func TestLinuxColdArchiveRoundTrip(t *testing.T) {
	// Check the observed result against the fixture's migration invariant.
	if runtime.GOOS != "linux" {
		t.Skip("GNU tar filesystem gate runs inside the disposable Ubuntu VM")
	}
	source := t.TempDir()
	destination := t.TempDir()
	prepareArchiveFilesystem(t, source)
	archive, err := createArchive(t.Context(), source, filepath.Join(t.TempDir(), "cold.tar"))
	requireMigration(t, err)
	requireMigration(t, restoreArchive(t.Context(), archive, destination, false))
	checkArchiveFilesystem(t, source, destination)
}

// prepareArchiveFilesystem creates numeric permissions, hard links and a real sparse file.
func prepareArchiveFilesystem(t *testing.T, dir string) {
	t.Helper()
	requireMigration(t, os.WriteFile(filepath.Join(dir, "data"), []byte("durable-data"), 0640))
	requireMigration(t, os.Link(filepath.Join(dir, "data"), filepath.Join(dir, "hard")))
	requireMigration(t, os.Symlink("data", filepath.Join(dir, "link")))
	f, err := os.OpenFile(filepath.Join(dir, "sparse"), os.O_CREATE|os.O_WRONLY, 0600)
	requireMigration(t, err)
	_, err = f.Seek(8<<20, 0)
	requireMigration(t, err)
	_, err = f.Write([]byte{1})
	requireMigration(t, err)
	requireMigration(t, f.Close())
}

// checkArchiveFilesystem checks bytes, mode, sparse length and preserved link identity.
func checkArchiveFilesystem(t *testing.T, source, destination string) {
	t.Helper()
	original, err := os.ReadFile(filepath.Join(source, "sparse"))
	requireMigration(t, err)
	restored, err := os.ReadFile(filepath.Join(destination, "sparse"))
	requireMigration(t, err)
	// Check the observed result against the fixture's migration invariant.
	if string(original) != string(restored) {
		t.Fatal("sparse file data changed")
	}
	data, err := os.Stat(filepath.Join(destination, "data"))
	requireMigration(t, err)
	hard, err := os.Stat(filepath.Join(destination, "hard"))
	requireMigration(t, err)
	// Check the observed result against the fixture's migration invariant.
	if !os.SameFile(data, hard) || data.Mode().Perm() != 0640 {
		t.Fatal("hard link identity or permissions changed")
	}
	link, err := os.Readlink(filepath.Join(destination, "link"))
	requireMigration(t, err)
	// Check the observed result against the fixture's migration invariant.
	if link != "data" {
		t.Fatal("symlink changed", link)
	}
}
