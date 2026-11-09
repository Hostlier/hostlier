package artifact

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
)

// TestUploadStatusIsolatesDurablePrefixes verifies that a fresh store can resume only its owner's bytes.
func TestUploadStatusIsolatesDurablePrefixes(t *testing.T) {
	root := t.TempDir()
	owner := Store{Root: root, UploadOwner: "alice"}
	receipt := Result{Image: "hostlier.local/artifacts@sha256:" + strings.Repeat("a", 64), ConfigDigest: "sha256:" + strings.Repeat("b", 64), ArchiveDigest: strings.Repeat("c", 64), Size: 2 * ChunkBytes}
	data := bytes.Repeat([]byte("x"), ChunkBytes)
	// One full MiB must be accepted under the upgraded bounded protocol.
	if _, err := owner.Chunk(receipt.ArchiveDigest, 0, data); err != nil {
		t.Fatal(err)
	}
	fresh := Store{Root: root, UploadOwner: "alice"}
	status, err := fresh.Status(receipt)
	digest, _ := PrefixDigest(bytes.NewReader(data), int64(len(data)))
	// Reconstructing the store must preserve its exact durable byte count and checksum.
	if err != nil || status.Offset != ChunkBytes || status.PrefixDigest != digest || status.ChunkBytes != ChunkBytes {
		t.Fatal(status, err)
	}
	other := Store{Root: root, UploadOwner: "bob"}
	status, err = other.Status(receipt)
	// Another authenticated uploader cannot learn or resume the private uncommitted prefix.
	if err != nil || status.Offset != 0 || status.PrefixDigest != "" {
		t.Fatal(status, err)
	}
}

// TestUploadStatusVerifiesCommittedBytes prevents cache hits from weakening integrity checks.
func TestUploadStatusVerifiesCommittedBytes(t *testing.T) {
	receipt, err := writeImage(t.TempDir(), empty.Image)
	// Use real archive and manifest identities through commit and lookup.
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(receipt.Path)
	// Fixture bytes must be the exact content selected by the receipt.
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Root: t.TempDir(), UploadOwner: "alice"}
	uploadTestArchive(t, store, receipt.ArchiveDigest, data)
	// Publication verifies the transport and image independently.
	if _, err = store.Commit(receipt); err != nil {
		t.Fatal(err)
	}
	store.UploadOwner = "bob"
	status, err := store.Status(receipt)
	// Verified immutable content may be reused across authorized uploads.
	if err != nil || !status.Committed || status.Offset != receipt.Size {
		t.Fatal(status, err)
	}
	path := filepath.Join(store.Root, receipt.ArchiveDigest+".tar")
	// Simulate tampering after the archive was committed.
	if err = os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	// Existing metadata cannot authorize reuse after the archive is modified.
	if _, err = store.Status(receipt); err == nil {
		t.Fatal("corrupt committed image reused")
	}
}

// TestUploadStatusRejectsInvalidInputs prevents status negotiation from selecting arbitrary paths.
func TestUploadStatusRejectsInvalidInputs(t *testing.T) {
	store := Store{Root: t.TempDir(), UploadOwner: "alice"}
	// Invalid sizes and digests must fail before inspecting controller files.
	for _, receipt := range []Result{{}, {ArchiveDigest: "../escape"}, {Size: -1}} {
		// An invalid request must not create or inspect a staging object.
		if _, err := store.Status(receipt); err == nil {
			t.Fatal("invalid receipt accepted", receipt)
		}
	}
	large := Result{Image: "hostlier.local/artifacts@sha256:" + strings.Repeat("a", 64), ConfigDigest: "sha256:" + strings.Repeat("b", 64), ArchiveDigest: strings.Repeat("c", 64), Size: (512 << 20) + 1}
	if !ValidReceipt(large) {
		t.Fatal("rejected valid large image receipt")
	}
}
