package artifact

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
)

// TestOwnedUploads isolates unverified bytes while retaining verified shared images and commit retries.
func TestOwnedUploads(t *testing.T) {
	receipt, err := writeImage(t.TempDir(), empty.Image)
	// Create verified image bytes without a registry or runtime dependency.
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(receipt.Path)
	// The upload must use the exact fixture archive.
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	alice := Store{Root: root, UploadOwner: "alice"}
	bob := Store{Root: root, UploadOwner: "bob"}
	shared := Store{Root: root}
	// A malicious prefix for the same claimed digest must not poison another uploader.
	if _, err = bob.Chunk(receipt.ArchiveDigest, 0, []byte("invalid")); err != nil {
		t.Fatal(err)
	}
	uploadTestArchive(t, alice, receipt.ArchiveDigest, data)
	// Another owner's malformed staging must not block publication.
	if _, err = alice.Commit(receipt); err != nil {
		t.Fatal(err)
	}
	// A lost commit acknowledgement is recoverable after staging has moved.
	if _, err = alice.Commit(receipt); err != nil {
		t.Fatal("commit retry", err)
	}
	// Existing shared content cannot conceal invalid bytes in the caller's staging.
	if _, err = bob.Commit(receipt); err == nil {
		t.Fatal("invalid staged bytes committed")
	}
	// A fresh scoped upload cannot append to the published archive.
	if _, err = alice.Chunk(receipt.ArchiveDigest, int64(len(data)), []byte("tamper")); err == nil {
		t.Fatal("appended to committed content")
	}
	// The runtime must still observe intact shared content after the append attempt.
	if _, err = shared.Inspect(receipt.Image); err != nil {
		t.Fatal("shared image damaged", err)
	}
	// Reuploading identical bytes stays compatible with clients that always start at zero.
	uploadTestArchive(t, alice, receipt.ArchiveDigest, data)
	// Publishing a complete identical reupload is safe.
	if _, err = alice.Commit(receipt); err != nil {
		t.Fatal("reupload", err)
	}
}

// TestOwnedUploadsHaveNoFixedQuota admits another actor when a sparse old archive exceeds the former cap.
func TestOwnedUploadsHaveNoFixedQuota(t *testing.T) {
	root := t.TempDir()
	file, err := os.Create(filepath.Join(root, "upload-other.tar"))
	// A preexisting sparse archive represents another client's old quota usage.
	if err != nil {
		t.Fatal(err)
	}
	// Sparse data tests removal of the fixed two GiB quota without consuming space.
	err = file.Truncate(2 << 30)
	file.Close()
	// Filesystem support is required for meaningful size verification.
	if err != nil {
		t.Fatal(err)
	}
	s := Store{Root: root, UploadOwner: "../new-owner"}
	// Disk availability, rather than a fixed aggregate cap, governs new uploads.
	if _, err = s.Chunk("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, []byte("x")); err != nil {
		t.Fatal("fixed quota still blocked upload", err)
	}
}
