package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
)

// artifactUploadFixture uses the real disk store behind an injectable CLI transport.
type artifactUploadFixture struct {
	store      artifact.Store
	receipt    artifact.Result
	offsets    []int64
	sizes      []int
	failFrom   int64
	dropOnce   bool
	dropCommit bool
	statusErr  error
	commits    int
}

// uploadReceipt creates a real multi-chunk image archive without registry access.
func uploadReceipt(t *testing.T) artifact.Result {
	t.Helper()
	image, err := random.Image(2*artifact.ChunkBytes, 1)
	// A legitimate image exercises independent archive, manifest and config checks.
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "image.tar")
	tag, _ := name.NewTag("hostlier.local/test:upload")
	// Serialize the portable format used by production preparation.
	if err = tarball.WriteToFile(path, tag, image); err != nil {
		t.Fatal(err)
	}
	image, err = tarball.ImageFromPath(path, nil)
	// Derive the canonical manifest reconstructed by the server's verifier.
	if err != nil {
		t.Fatal(err)
	}
	digest, err := image.Digest()
	// Preserve errors instead of producing a fixture with missing identity.
	if err != nil {
		t.Fatal(err)
	}
	config, _ := image.ConfigName()
	checksum, size, err := artifact.FileDigest(path)
	// Ensure tests really cross the new chunk boundary.
	if err != nil || size <= artifact.ChunkBytes {
		t.Fatal(size, err)
	}
	return artifact.Result{Image: "hostlier.local/artifacts@" + digest.String(), ConfigDigest: config.String(), ArchiveDigest: checksum, Size: size, Path: path}
}

// reply implements only the staging protocol and tracks observable network work.
func (f *artifactUploadFixture) reply(c call) (any, error) {
	// Each test request must stay within the inert artifact preparation surface.
	switch {
	case c.path == "/v1/artifacts/status": // Simulate both upgraded and older controllers.
		if f.statusErr != nil {
			return nil, f.statusErr
		}
		return f.store.Status(f.receipt)
	case strings.HasSuffix(c.path, "/chunks"): // Use actual replay and durable-offset checks.
		return f.chunk(c.body.(map[string]any))
	case c.path == "/v1/artifacts/commit": // Simulate an acknowledgement lost after publication.
		f.commits++
		result, err := f.store.Commit(f.receipt)
		// The next invocation must recover through verified status, without retransmission.
		if err == nil && f.dropCommit {
			return nil, model.Err("CONNECTION", "Lost commit acknowledgement")
		}
		return result, err
	default: // No deployment or approval call belongs to artifact transfer.
		return nil, fmt.Errorf("unexpected request %s", c.path)
	}
}

// chunk records transmitted bytes and injects disconnects before or after a durable write.
func (f *artifactUploadFixture) chunk(body map[string]any) (any, error) {
	offset, data := body["offset"].(int64), body["data"].([]byte)
	f.offsets = append(f.offsets, offset)
	f.sizes = append(f.sizes, len(data))
	// Persistent disconnects exhaust the ordinary retry and leave a resumable file.
	if f.failFrom > 0 && offset >= f.failFrom {
		return nil, model.Err("CONNECTION", "Disconnected")
	}
	count, err := f.store.Chunk(f.receipt.ArchiveDigest, offset, data)
	// A lost reply must replay identical bytes rather than append them twice.
	if err == nil && f.dropOnce {
		f.dropOnce = false
		return nil, model.Err("CONNECTION", "Lost chunk acknowledgement")
	}
	return map[string]int64{"offset": count}, err
}

// session creates a fresh CLI invocation while leaving the server's store untouched.
func (f *artifactUploadFixture) session() *session {
	return &session{ctx: context.Background(), remote: &fakeClient{reply: f.reply}, flags: map[string]string{}, errOut: io.Discard}
}

// TestArtifactLargeChunksAndServerCache proves request reduction and zero-byte repeat transfers.
func TestArtifactLargeChunksAndServerCache(t *testing.T) {
	f := &artifactUploadFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "owner"}, receipt: uploadReceipt(t)}
	_, err := f.session().transferArtifact(f.receipt)
	want := int((f.receipt.Size + artifact.ChunkBytes - 1) / artifact.ChunkBytes)
	// New controllers should require exactly one request per MiB, plus the final short chunk.
	if err != nil || len(f.offsets) != want || f.sizes[0] != artifact.ChunkBytes {
		t.Fatal(err, f.offsets, f.sizes)
	}
	_, err = f.session().transferArtifact(f.receipt)
	// Repeated invocations must skip both upload and commit after verified server status.
	if err != nil || len(f.offsets) != want || f.commits != 1 {
		t.Fatal(err, f.offsets, f.commits)
	}
}

// TestArtifactResumeAfterInterruptedInvocation verifies retained-prefix recovery across fresh clients.
func TestArtifactResumeAfterInterruptedInvocation(t *testing.T) {
	f := &artifactUploadFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "owner"}, receipt: uploadReceipt(t), failFrom: artifact.ChunkBytes}
	_, err := f.session().transferArtifact(f.receipt)
	// The simulated disconnect must leave the upload incomplete rather than commit it.
	if err == nil || f.commits != 0 {
		t.Fatal("interrupted upload succeeded")
	}
	f.failFrom, f.offsets, f.sizes = 0, nil, nil
	_, err = f.session().transferArtifact(f.receipt)
	// A fresh invocation starts at the retained MiB boundary, not byte zero.
	if err != nil || len(f.offsets) == 0 || f.offsets[0] != artifact.ChunkBytes {
		t.Fatal(err, f.offsets)
	}
	// The independently verified final content must remain usable for deployment.
	if _, err = f.store.Inspect(f.receipt.Image); err != nil {
		t.Fatal(err)
	}
}

// TestArtifactLostAcknowledgements preserves exact replay and committed-cache recovery.
func TestArtifactLostAcknowledgements(t *testing.T) {
	f := &artifactUploadFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "owner"}, receipt: uploadReceipt(t), dropOnce: true, dropCommit: true}
	_, err := f.session().transferArtifact(f.receipt)
	// A dropped first reply should replay offset zero exactly; commit remains ambiguous to this caller.
	if err == nil || len(f.offsets) < 2 || f.offsets[0] != 0 || f.offsets[1] != 0 {
		t.Fatal(err, f.offsets)
	}
	count := len(f.offsets)
	_, err = f.session().transferArtifact(f.receipt)
	// A new status check must confirm the completed commit without another byte or commit call.
	if err != nil || len(f.offsets) != count || f.commits != 1 {
		t.Fatal(err, f.offsets, f.commits)
	}
}

// TestArtifactLegacyFallback uses small chunks only for an explicitly unsupported status endpoint.
func TestArtifactLegacyFallback(t *testing.T) {
	f := &artifactUploadFixture{store: artifact.Store{Root: t.TempDir()}, receipt: uploadReceipt(t), statusErr: model.Err("NOT_FOUND", "Artifact endpoint not found")}
	_, err := f.session().transferArtifact(f.receipt)
	want := int((f.receipt.Size + artifact.LegacyChunkBytes - 1) / artifact.LegacyChunkBytes)
	// Old servers must never receive a chunk larger than their original limit.
	if err != nil || len(f.sizes) != want || f.sizes[0] != artifact.LegacyChunkBytes {
		t.Fatal(err, f.sizes)
	}
	f.statusErr = model.Err("SCOPE_DENIED", "Revoked")
	_, err = f.session().transferArtifact(f.receipt)
	// Authorization failures must not be mistaken for protocol-version negotiation.
	if err == nil || len(f.sizes) != want {
		t.Fatal("authorization failure downgraded", err)
	}
}

// TestArtifactResumeRejectsWrongPrefix fails before appending to unrelated retained bytes.
func TestArtifactResumeRejectsWrongPrefix(t *testing.T) {
	f := &artifactUploadFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "owner"}, receipt: uploadReceipt(t)}
	_, err := f.store.Chunk(f.receipt.ArchiveDigest, 0, bytes.Repeat([]byte("x"), 123))
	// The controller may hold a partial uncommitted upload, but its bytes must match locally.
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.session().transferArtifact(f.receipt)
	// Prefix validation must precede every resumed write and final commit.
	if err == nil || len(f.sizes) != 0 || f.commits != 0 {
		t.Fatal(err, f.sizes, f.commits)
	}
}

// TestArtifactResumeLegacyPrefix permits switching to large chunks after an older client's partial upload.
func TestArtifactResumeLegacyPrefix(t *testing.T) {
	f := &artifactUploadFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "owner"}, receipt: uploadReceipt(t)}
	data, err := os.ReadFile(f.receipt.Path)
	// The retained prefix must be actual bytes from this immutable archive.
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Chunk(f.receipt.ArchiveDigest, 0, data[:artifact.LegacyChunkBytes])
	// Legacy chunk writes remain accepted by upgraded stores.
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.session().transferArtifact(f.receipt)
	// Resume positions need not be aligned to the newly negotiated chunk size.
	if err != nil || f.offsets[0] != artifact.LegacyChunkBytes || f.sizes[0] != artifact.ChunkBytes {
		t.Fatal(err, f.offsets, f.sizes)
	}
}

// TestArtifactInvalidStatusRejectsProgress prevents malformed metadata from skipping or overallocating transfers.
func TestArtifactInvalidStatusRejectsProgress(t *testing.T) {
	// Each response violates an independent protocol invariant.
	for _, status := range []artifact.UploadStatus{
		{ChunkBytes: 0},
		{ChunkBytes: artifact.ChunkBytes + 1},
		{ChunkBytes: artifact.ChunkBytes, Offset: -1},
		{ChunkBytes: artifact.ChunkBytes, Offset: 11},
		{ChunkBytes: artifact.ChunkBytes, Committed: true, Offset: 9},
	} {
		// Return the invalid negotiated metadata without touching a real file.
		client := &fakeClient{reply: func(call) (any, error) { return status, nil }}
		s := &session{ctx: context.Background(), remote: client, flags: map[string]string{}}
		_, err := s.artifactUploadStatus(artifact.Result{Size: 10})
		// Invalid metadata must fail before allocating a buffer or attempting writes.
		if err == nil {
			t.Fatal("invalid status accepted", status)
		}
	}
}

// TestArtifactImpossibleAcknowledgements rejects both lost bytes and fabricated extra progress.
func TestArtifactImpossibleAcknowledgements(t *testing.T) {
	// One-byte writes cannot acknowledge zero bytes or more than the complete artifact.
	for _, offset := range []int64{0, 11} {
		// Only the acknowledgement is faulty; request transport itself succeeds.
		client := &fakeClient{reply: func(call) (any, error) { return map[string]int64{"offset": offset}, nil }}
		s := &session{ctx: context.Background(), remote: client, flags: map[string]string{}}
		err := s.sendArtifactChunk(artifact.Result{Size: 10}, 0, []byte("x"))
		// Impossible progress cannot reach commit.
		if err == nil {
			t.Fatal("invalid acknowledgement accepted", offset)
		}
	}
}
