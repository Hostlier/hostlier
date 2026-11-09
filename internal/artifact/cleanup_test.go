package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// cleanupImage creates an independently verified committed image with a controlled idle lease.
func cleanupImage(t *testing.T, store Store, version string, modified time.Time) Result {
	t.Helper()
	image, err := mutate.Config(empty.Image, v1.Config{Env: []string{"VERSION=" + version}})
	// Check the fixture or cleanup invariant before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := writeImage(t.TempDir(), image)
	// Check the fixture or cleanup invariant before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(receipt.Path)
	// Check the fixture or cleanup invariant before proceeding.
	if err != nil {
		t.Fatal(err)
	}
	uploadTestArchive(t, store, receipt.ArchiveDigest, data)
	// Check the fixture or cleanup invariant before proceeding.
	if _, err = store.Commit(receipt); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	for _, name := range []string{receipt.ArchiveDigest + ".tar", strings.TrimPrefix(receipt.Image, "hostlier.local/artifacts@sha256:") + ".json"} {
		// Check the fixture or cleanup invariant before proceeding.
		if err = os.Chtimes(filepath.Join(store.Root, name), modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	return receipt
}

// TestSweepRetainsLiveAndRecentContent reclaims only old unreferenced managed files and leaves durable cache eviction intent.
func TestSweepRetainsLiveAndRecentContent(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * CleanupGrace)
	s := Store{Root: t.TempDir(), UploadOwner: "client"}
	live := cleanupImage(t, s, "live", old)
	unused := cleanupImage(t, s, "unused", old)
	recent := cleanupImage(t, s, "recent", now)
	id := strings.Repeat("a", 64)
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := s.Chunk(id, 0, []byte("abandoned")); err != nil {
		t.Fatal(err)
	}
	path, _ := s.chunkPath(id)
	// Check the fixture or cleanup invariant before proceeding.
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := s.Sweep(now, map[string]bool{live.Image: true})
	// Check the fixture or cleanup invariant before proceeding.
	if err != nil || result.Files != 3 {
		t.Fatal(result, err)
	}
	assertSweepPreservation(t, s, now, live, unused, recent)
}

// assertSweepPreservation checks shared content, persistent eviction and repeatability after the first sweep.
func assertSweepPreservation(t *testing.T, s Store, now time.Time, live, unused, recent Result) {
	t.Helper()
	var err error
	var result CleanupResult
	// Check the fixture or cleanup invariant before proceeding.
	for _, image := range []string{live.Image, recent.Image} {
		// Check the fixture or cleanup invariant before proceeding.
		if _, err = s.Inspect(image); err != nil {
			t.Fatal("protected image removed", err)
		}
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err = s.Inspect(unused.Image); err == nil {
		t.Fatal("unused image retained")
	}
	restarted := Store{Root: s.Root}
	images, err := restarted.PendingImages()
	// Check the fixture or cleanup invariant before proceeding.
	if err != nil || len(images) != 1 || images[0] != unused.Image {
		t.Fatal("eviction intent lost", images, err)
	}
	result, err = restarted.Sweep(now, map[string]bool{live.Image: true})
	// Check the fixture or cleanup invariant before proceeding.
	if err != nil || result.Files != 0 {
		t.Fatal("non-idempotent sweep", result, err)
	}
}

// TestSweepFailsClosedOnCorruptReceipt protects all archive data when references cannot be trusted.
func TestSweepFailsClosedOnCorruptReceipt(t *testing.T) {
	s := Store{Root: t.TempDir()}
	archive := filepath.Join(s.Root, strings.Repeat("b", 64)+".tar")
	// Check the fixture or cleanup invariant before proceeding.
	if err := os.WriteFile(archive, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * CleanupGrace)
	// Check the fixture or cleanup invariant before proceeding.
	if err := os.Chtimes(archive, old, old); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if err := os.WriteFile(filepath.Join(s.Root, strings.Repeat("c", 64)+".json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := s.Sweep(time.Now(), nil); err == nil {
		t.Fatal("corrupt catalog accepted")
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := os.Stat(archive); err != nil {
		t.Fatal("archive removed after catalog error", err)
	}
}

// TestSweepIgnoresLinksAndUnknownFiles never follows deletion candidates outside the managed store.
func TestSweepIgnoresLinksAndUnknownFiles(t *testing.T) {
	s := Store{Root: t.TempDir()}
	outside := filepath.Join(t.TempDir(), "keep")
	// Check the fixture or cleanup invariant before proceeding.
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if err := os.Symlink(outside, filepath.Join(s.Root, strings.Repeat("d", 64)+".tar")); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := s.Sweep(time.Now().Add(2*CleanupGrace), nil); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}
}

// TestUploadReplayRenewsLease preserves resumability without checking disk capacity again.
func TestUploadReplayRenewsLease(t *testing.T) {
	s := Store{Root: t.TempDir(), UploadOwner: "client"}
	id := strings.Repeat("e", 64)
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := s.Chunk(id, 0, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	path, _ := s.chunkPath(id)
	old := time.Now().Add(-2 * CleanupGrace)
	// Check the fixture or cleanup invariant before proceeding.
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := s.Chunk(id, 0, []byte("hello")); err != nil {
		t.Fatal("replay failed", err)
	}
	info, err := os.Stat(path)
	// Check the fixture or cleanup invariant before proceeding.
	if err != nil || time.Since(info.ModTime()) > time.Minute {
		t.Fatal("lease not renewed", err)
	}
}
