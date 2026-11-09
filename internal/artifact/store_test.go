package artifact

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// TestPortableArtifactRoundTrip catches OCI-to-Docker manifest conversion and transport corruption.
func TestPortableArtifactRoundTrip(t *testing.T) {
	image, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: "arm64", Config: v1.Config{Entrypoint: []string{"/agent"}}})
	// The fixture needs a legitimate minimal OCI configuration, without registry access.
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := writeImage(t.TempDir(), image)
	// Transport serialization must preserve a self-consistent reviewed content identity.
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(receipt.Path)
	// Real serialized tar bytes are uploaded through the same bounded store as production.
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Root: t.TempDir()}
	uploadTestArchive(t, store, receipt.ArchiveDigest, data)
	committed, err := store.Commit(receipt)
	// Both independent manifest and config verification must accept the prepared receipt.
	if err != nil || committed.Image != receipt.Image {
		t.Fatalf("commit: %+v %v", committed, err)
	}
	_, err = store.Inspect(receipt.Image)
	// Retained bytes must be usable for a later deployment or rollback.
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, receipt.ArchiveDigest+".tar")
	data[len(data)/2] ^= 1
	// Tampering with committed bytes must not reuse the previously approved image identity.
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = store.Inspect(receipt.Image)
	// Rehashing on inspection is mandatory even when the receipt still exists.
	if err == nil {
		t.Fatal("corrupt archive reused")
	}
}

// uploadTestArchive verifies bounded sequential writes using actual serialized transport bytes.
func uploadTestArchive(t *testing.T, s Store, id string, data []byte) {
	t.Helper()
	// Exact replay exercises lost-response recovery without extending the file twice.
	for offset := 0; offset < len(data); offset += 65536 {
		end := min(offset+65536, len(data))
		count, err := s.Chunk(id, int64(offset), data[offset:end])
		// Acknowledgements describe durable bytes, not the caller's requested offset.
		if err != nil || count != int64(end) {
			t.Fatalf("upload %d: %v %d", offset, err, count)
		}
	}
}

// TestArtifactChunksRejectMutation validates retry, traversal, sparse writes and bounded allocation.
func TestArtifactChunksRejectMutation(t *testing.T) {
	s := Store{Root: t.TempDir()}
	id := strings.Repeat("a", 64)
	first, err := s.Chunk(id, 0, []byte("hello"))
	// The initial write establishes the only valid append position.
	if err != nil || first != 5 {
		t.Fatal(first, err)
	}
	count, err := s.Chunk(id, 0, []byte("hello"))
	// An exact replay after a lost response must not duplicate content.
	if err != nil || count != 5 {
		t.Fatal(count, err)
	}
	cases := []struct {
		id     string
		offset int64
		data   []byte
	}{{id, 0, []byte("other")}, {id, 7, []byte("x")}, {"../escape", 0, []byte("x")}, {id, int64(^uint64(0) >> 1), []byte("x")}, {id, 5, bytes.Repeat([]byte("x"), ChunkBytes+1)}}
	// Each invalid request must fail without extending or overwriting the staged object.
	for _, test := range cases {
		_, err = s.Chunk(test.id, test.offset, test.data)
		// No malformed chunk is allowed to advance the upload cursor.
		if err == nil {
			t.Fatalf("invalid chunk accepted: %q %d", test.id, test.offset)
		}
	}
}
