package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRecommitCancelsPendingEviction renews retention after an earlier cache-removal failure.
func TestRecommitCancelsPendingEviction(t *testing.T) {
	s := Store{Root: t.TempDir(), UploadOwner: "actor"}
	receipt := cleanupImage(t, s, "retry", time.Now().Add(-2*CleanupGrace))
	archive, err := os.ReadFile(filepath.Join(s.Root, receipt.ArchiveDigest+".tar"))
	// Keep transport bytes so the test can simulate a later verified upload.
	if err != nil {
		t.Fatal(err)
	}
	// Expiry must succeed before retrying the same upload.
	if _, err = s.Sweep(time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.Root, strings.TrimPrefix(receipt.Image, "hostlier.local/artifacts@sha256:")+".gc")
	// The first expiry leaves a durable image eviction intent.
	if _, err = os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	uploadTestArchive(t, s, receipt.ArchiveDigest, archive)
	invalid := receipt
	invalid.ConfigDigest = "sha256:" + strings.Repeat("0", 64)
	// Failed validation must not cancel the existing cleanup intent.
	if _, err = s.Commit(invalid); err == nil {
		t.Fatal("invalid receipt accepted")
	}
	// The pending deletion marker must remain durable.
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("failed commit lost pending cleanup", err)
	}
	// Only a fully verified recommit replaces the previous retention decision.
	if _, err = s.Commit(receipt); err != nil {
		t.Fatal(err)
	}
	// Successful recommit must clear the stale deletion marker.
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("fresh upload still pending eviction", err)
	}
	// The recommitted image must remain deployable.
	if _, err = s.Inspect(receipt.Image); err != nil {
		t.Fatal(err)
	}
}
