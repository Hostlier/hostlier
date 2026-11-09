package packageexec

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// uploadCleanupFixture creates a canonical private upload directory and one retained transfer.
func uploadCleanupFixture(t *testing.T) (Uploads, string, []byte) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	// Mirror the production store's nonredirected ancestry.
	if err != nil {
		t.Fatal(err)
	}
	s := Uploads{Root: filepath.Join(root, "uploads")}
	data := []byte("unfinished package upload")
	// A successful write establishes the same file identity used by periodic cleanup.
	if _, err := s.Chunk("actor", Digest(data), 0, data); err != nil {
		t.Fatal(err)
	}
	path, err := s.path("actor", Digest(data))
	// Fixture paths must come from the store rather than manual user-controlled names.
	if err != nil {
		t.Fatal(err)
	}
	return s, path, data
}

// ageUpload gives a managed file a deterministic expired idle lease.
func ageUpload(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-2 * UploadGrace)
	// Timestamp changes simulate interruption without waiting for real expiry.
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// TestUploadSweepReclaimsWithoutNewTraffic expires abandoned transfers while retaining recent ones.
func TestUploadSweepReclaimsWithoutNewTraffic(t *testing.T) {
	s, path, data := uploadCleanupFixture(t)
	// Another actor's recent transfer remains resumable.
	if _, err := s.Chunk("recent", Digest(data), 0, data); err != nil {
		t.Fatal(err)
	}
	ageUpload(t, path)
	result, err := s.Sweep(time.Now())
	// Only the abandoned transfer contributes to confirmed reclaimed bytes.
	if err != nil || result.Files != 1 || result.Bytes != int64(len(data)) {
		t.Fatal(result, err)
	}
	recent, _ := s.path("recent", Digest(data))
	// Successful cleanup cannot remove unrelated recent staging.
	if _, err := os.Stat(recent); err != nil {
		t.Fatal(err)
	}
	// Repeating cleanup after restart must be harmless.
	result, err = (Uploads{Root: s.Root}).Sweep(time.Now())
	// No additional file should be reclaimed during this sweep.
	if err != nil || result.Files != 0 {
		t.Fatal(result, err)
	}
}

// TestUploadUseRenewsCleanupLease preserves retries and verified approval previews.
func TestUploadUseRenewsCleanupLease(t *testing.T) {
	// Each operation must independently renew an otherwise expired transfer.
	for _, operation := range []string{"replay", "preview", "invalid replay"} {
		t.Run(operation, func(t *testing.T) { // Each case owns independent staging and timestamps.
			s, path, data := uploadCleanupFixture(t)
			ageUpload(t, path)
			var useErr error
			// Replays and preview reads both prove continued use of the exact retained bytes.
			switch operation {
			case "replay": // Identical retained bytes remain resumable.
				_, useErr = s.Chunk("actor", Digest(data), 0, data)
			case "preview": // Successful review preserves the upcoming approval window.
				_, useErr = s.Read("actor", InstallRequest{Name: "postgres", Digest: Digest(data), Size: int64(len(data))})
			case "invalid replay": // Rejected input must not extend retention.
				_, useErr = s.Chunk("actor", Digest(data), 0, []byte("changed"))
			}
			// Verify that the intended success or rejection actually occurred.
			if (useErr != nil) != (operation == "invalid replay") {
				t.Fatal(useErr)
			}
			result, err := s.Sweep(time.Now())
			// A rejected replay cannot prolong the abandoned data's lease.
			if err != nil || (result.Files == 1) != (operation == "invalid replay") {
				t.Fatal(result, err)
			}
		})
	}
}

// TestUploadSweepPreservesUnknownFilesAndLinks bounds cleanup to recognized inert staging.
func TestUploadSweepPreservesUnknownFilesAndLinks(t *testing.T) {
	s, path, _ := uploadCleanupFixture(t)
	unknown := filepath.Join(s.Root, "notes")
	// Non-upload files remain outside the collector's authority even when old.
	if err := os.WriteFile(unknown, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ageUpload(t, unknown)
	link := filepath.Join(s.Root, Digest([]byte("other"))+"-"+Digest([]byte("link")))
	// A recognized filename is insufficient authority to follow a filesystem link.
	if err := os.Symlink(unknown, link); err != nil {
		t.Fatal(err)
	}
	result, err := s.Sweep(time.Now())
	// Recent uploads, unknown content and links must all survive.
	if err != nil || result.Files != 0 {
		t.Fatal(result, err)
	}
	assertUploadFilesExist(t, path, unknown, link)
	redirected := Uploads{Root: filepath.Join(filepath.Dir(s.Root), "redirect")}
	// Redirected roots must not authorize deletion through an alias.
	if err := os.Symlink(s.Root, redirected.Root); err != nil {
		t.Fatal(err)
	}
	// A redirected store must fail before it can remove content.
	if _, err := redirected.Sweep(time.Now()); err == nil {
		t.Fatal("redirected root accepted")
	}
}

// assertUploadFilesExist checks retained files without following symlinks.
func assertUploadFilesExist(t *testing.T, names ...string) {
	t.Helper()
	// Each supplied path must remain present after cleanup.
	for _, name := range names {
		// Retained paths must still exist, including the link itself.
		if _, err := os.Lstat(name); err != nil {
			t.Fatal(name, err)
		}
	}
}

// TestUploadQuotaPreservesUnknownFiles applies the same deletion boundary during lazy cleanup.
func TestUploadQuotaPreservesUnknownFiles(t *testing.T) {
	s, _, _ := uploadCleanupFixture(t)
	unknown := filepath.Join(s.Root, "notes")
	// Unknown content must remain accounted for rather than being treated as an upload.
	if err := os.WriteFile(unknown, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ageUpload(t, unknown)
	// Starting another upload invokes the existing lazy quota cleanup path.
	if _, err := s.Chunk("new", Digest([]byte("new")), 0, []byte("new")); err != nil {
		t.Fatal(err)
	}
	assertUploadFilesExist(t, unknown)
}
