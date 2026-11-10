package migration

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

// maliciousArchive keeps this migration step within its validated recovery boundary.
func maliciousArchive(t *testing.T, header *tar.Header) Archive {
	t.Helper()
	file := filepath.Join(t.TempDir(), "test.tar")
	f, err := os.Create(file)
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
	w := tar.NewWriter(f)
	// Check the observed result against the fixture's migration invariant.
	if err := w.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	w.Close()
	f.Close()
	a, err := hashFile(file)
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestArchiveRejectsTraversalLinksSpecialFilesAndCorruption keeps this migration step within its validated recovery boundary.
func TestArchiveRejectsTraversalLinksSpecialFilesAndCorruption(t *testing.T) {
	// Exercise each fixture case against the same recovery invariant.
	for _, header := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0600},
		{Name: "/absolute", Typeflag: tar.TypeReg, Mode: 0600},
		{Name: "nested/link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "hard", Typeflag: tar.TypeLink, Linkname: "../escape"},
		{Name: "device", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3},
	} {
		// Supply isolated fixture behavior without contacting a live server.
		t.Run(header.Name+header.Linkname, func(t *testing.T) {
			// Check the observed result against the fixture's migration invariant.
			if err := verifyArchive(maliciousArchive(t, header)); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	a := maliciousArchive(t, &tar.Header{Name: "safe", Typeflag: tar.TypeReg, Mode: 0600})
	// Check the observed result against the fixture's migration invariant.
	if err := verifyArchive(a); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(a.File, []byte("corrupted"), 0600)
	// Check the observed result against the fixture's migration invariant.
	if err := verifyArchive(a); err == nil {
		t.Fatal("backup corruption ignored")
	}
}

// TestMigrationRejectsSymlinkedAndNestedDestinations keeps this migration step within its validated recovery boundary.
func TestMigrationRejectsSymlinkedAndNestedDestinations(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	os.Mkdir(state, 0700)
	base := Options{StateDir: state, Config: filepath.Join(root, "config"), BackupDir: filepath.Join(root, "backups")}
	// Check the observed result against the fixture's migration invariant.
	if err := validateOptions(base); err != nil {
		t.Fatal(err)
	}
	// Exercise each fixture case against the same recovery invariant.
	for _, bad := range []string{state, filepath.Join(state, "backups"), "/", filepath.Join(root, "bad\npath")} {
		o := base
		o.BackupDir = bad
		// Check the observed result against the fixture's migration invariant.
		if err := validateOptions(o); err == nil {
			t.Fatal("unsafe backup path accepted", bad)
		}
	}
	link := filepath.Join(root, "redirect")
	os.Symlink(state, link)
	base.BackupDir = filepath.Join(link, "backup")
	// Check the observed result against the fixture's migration invariant.
	if err := validateOptions(base); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
}

// TestStartupFailsClosedOnUnresolvedOrCorruptJournal keeps this migration step within its validated recovery boundary.
func TestStartupFailsClosedOnUnresolvedOrCorruptJournal(t *testing.T) {
	e, _, p := engineFixture(t)
	// Supply isolated fixture behavior without contacting a live server.
	e.progress = func(phase string) {
		// Check the observed result against the fixture's migration invariant.
		if phase == "backed-up" {
			panic("crash")
		}
	}
	// Supply isolated fixture behavior without contacting a live server.
	func() { defer func() { recover() }(); _ = e.apply(t.Context(), p) }()
	// Check the observed result against the fixture's migration invariant.
	if err := CheckStartup(p.StateDir); err == nil {
		t.Fatal("unresolved migration permitted startup")
	}
	os.WriteFile(filepath.Join(p.StateDir, "migration.json"), []byte("{}"), 0600)
	// Check the observed result against the fixture's migration invariant.
	if err := CheckStartup(p.StateDir); err == nil {
		t.Fatal("corrupt migration permitted startup")
	}
}
