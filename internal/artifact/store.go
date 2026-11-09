package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"hostlier/internal/model"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var digestRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

// IsImage distinguishes locally prepared content from registry pulls.
func IsImage(image string) bool { return strings.HasPrefix(image, "hostlier.local/artifacts@sha256:") }

// RuntimeImage returns the immutable-content tag installed by both importers.
func RuntimeImage(image string) string {
	// Ordinary registry images retain their digest-based identity.
	if !IsImage(image) {
		return image
	}
	return "hostlier.local/artifacts:" + strings.TrimPrefix(image, "hostlier.local/artifacts@sha256:")
}

// Store scopes artifact uploads and retained content beneath controller-owned storage.
type Store struct {
	Root        string
	UploadOwner string
}

// chunkPath permits only hash-derived paths beneath the protected artifact directory.
func (s Store) chunkPath(id string) (string, error) {
	// Reject traversal and ambiguous identifiers before touching the filesystem.
	if !digestRE.MatchString(id) {
		return "", model.Err("ARTIFACT_ID", "Invalid artifact digest")
	}
	// Staged content is administrator-only and never executable here.
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return "", err
	}
	// Hash the authenticated owner so untrusted identity text never becomes a path.
	if s.UploadOwner != "" {
		return filepath.Join(s.Root, fmt.Sprintf("upload-%x-%s.tar", sha256.Sum256([]byte(s.UploadOwner)), id)), nil
	}
	return filepath.Join(s.Root, id+".tar"), nil
}

// Chunk appends bounded data and verifies exact replay bytes for interrupted uploads.
func (s Store) Chunk(id string, offset int64, data []byte) (int64, error) {
	// Keep individual API requests small and offsets representable.
	if invalidChunk(offset, data) {
		return 0, model.Err("ARTIFACT_LIMIT", "Invalid artifact chunk")
	}
	path, err := s.chunkPath(id)
	// Path validation precedes all writes.
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	// An unavailable staging file must not advance the upload cursor.
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	// Persisted size is the authoritative resumable offset.
	if err != nil {
		return 0, err
	}
	// Exact duplicate chunks are safe after a lost response.
	if offset < info.Size() {
		count, err := verifyChunkReplay(f, offset, data, info.Size())
		// Successful replay renews the idle lease without consuming more disk space.
		if err == nil {
			err = os.Chtimes(path, time.Now(), time.Now())
		}
		return count, err
	}
	// Out-of-order or missing chunks cannot create sparse payloads.
	if offset != info.Size() {
		return info.Size(), model.Err("ARTIFACT_OFFSET", "Upload offset does not match retained bytes")
	}
	// Reserve free disk space before each append; exact retries need no new space.
	if err = s.checkAvailableSpace(int64(len(data))); err != nil {
		return info.Size(), err
	}
	var written int
	written, err = f.WriteAt(data, offset)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	// Sync the append before acknowledging durable progress.
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		return info.Size(), err
	}
	return offset + int64(len(data)), err
}

// verifyChunkReplay rejects attempts to overwrite a previously acknowledged prefix.
func verifyChunkReplay(f *os.File, offset int64, data []byte, size int64) (int64, error) {
	previous := make([]byte, len(data))
	_, err := f.ReadAt(previous, offset)
	// Replay is accepted only when the complete original chunk matches.
	if err != nil || !bytes.Equal(previous, data) {
		return size, model.Err("ARTIFACT_REPLAY", "Uploaded bytes differ from retained content")
	}
	return size, nil
}

// Commit validates archive and image identities before making content deployable.
func (s Store) Commit(receipt Result) (Result, error) {
	path, err := s.chunkPath(receipt.ArchiveDigest)
	// The archive checksum is also its safe storage identity.
	if err != nil {
		return Result{}, err
	}
	canonical := filepath.Join(s.Root, receipt.ArchiveDigest+".tar")
	// A completed commit can be retried after its private staging file was promoted.
	if s.committedUpload(path) {
		path = canonical
	}
	actual, size, err := FileDigest(path)
	// Incomplete, altered, or mis-sized uploads cannot become images.
	if err != nil || actual != receipt.ArchiveDigest || size != receipt.Size {
		return Result{}, model.Err("ARTIFACT_DIGEST", "Artifact archive verification failed")
	}
	digest, err := verifyImageReceipt(path, receipt)
	// Publish only independently verified image metadata.
	if err != nil {
		return Result{}, err
	}
	return s.publishReceipt(path, digest, receipt)
}

// publishReceipt promotes verified bytes before publishing the shared image identity.
func (s Store) publishReceipt(path, digest string, receipt Result) (Result, error) {
	canonical := filepath.Join(s.Root, receipt.ArchiveDigest+".tar")
	var err error
	// Only fully verified bytes may replace shared content with this exact identity.
	if path != canonical {
		// Failed promotion must never publish a receipt for unavailable content.
		if err = os.Rename(path, canonical); err != nil {
			return Result{}, err
		}
	}
	raw, _ := json.Marshal(receipt)
	target := filepath.Join(s.Root, digest+".json")
	// Atomically publish a receipt only after all content checks pass.
	if err = os.WriteFile(target+".tmp", raw, 0600); err != nil {
		return Result{}, err
	}
	// A failed receipt publication cannot cancel earlier cache cleanup intent.
	if err = os.Rename(target+".tmp", target); err != nil {
		return Result{}, err
	}
	// A verified recommit renews retention and supersedes any previous eviction marker.
	return receipt, s.ForgetImage(receipt.Image)
}

// committedUpload recognizes a scoped commit retry after staging was promoted.
func (s Store) committedUpload(path string) bool {
	_, err := os.Stat(path)
	return s.UploadOwner != "" && os.IsNotExist(err)
}

// Inspect loads and rehashes retained content before planning or importing it.
func (s Store) Inspect(image string) (Result, error) {
	id := strings.TrimPrefix(image, "hostlier.local/artifacts@sha256:")
	// Only canonical prepared image identifiers select metadata files.
	if !IsImage(image) || !digestRE.MatchString(id) {
		return Result{}, model.Err("ARTIFACT_ID", "Invalid prepared image identity")
	}
	f, err := os.Open(filepath.Join(s.Root, id+".json"))
	// Missing prepared content is not a reason to pull an unrelated registry image.
	if err != nil {
		return Result{}, model.Err("ARTIFACT_NOT_FOUND", "Prepared artifact unavailable on this server")
	}
	defer f.Close()
	var result Result
	err = json.NewDecoder(io.LimitReader(f, 4096)).Decode(&result)
	// Receipt identity must match the selected deployment input.
	if err != nil || result.Image != image || !digestRE.MatchString(result.ArchiveDigest) {
		return Result{}, model.Err("ARTIFACT_DIGEST", "Invalid artifact receipt")
	}
	result.Path = filepath.Join(s.Root, result.ArchiveDigest+".tar")
	digest, size, err := FileDigest(result.Path)
	// Rehash before import so altered content cannot reuse a previously approved receipt.
	if err != nil || digest != result.ArchiveDigest || size != result.Size {
		return Result{}, model.Err("ARTIFACT_DIGEST", "Retained artifact was modified")
	}
	return result, nil
}

// invalidChunk bounds arithmetic and transport allocation before opening a file.
func invalidChunk(offset int64, data []byte) bool {
	const maxOffset = int64(^uint64(0) >> 1)
	return len(data) == 0 || len(data) > ChunkBytes || offset < 0 || offset > maxOffset-int64(len(data))
}

// verifyImageReceipt compares both manifest and execution configuration identities.
func verifyImageReceipt(path string, receipt Result) (string, error) {
	image, err := tarball.ImageFromPath(path, nil)
	// Parse the archive without extracting files or running its contents.
	if err != nil {
		return "", model.Err("ARTIFACT_IMAGE", "Invalid image archive")
	}
	digest, err := image.Digest()
	// The manifest must be exactly the one presented for deployment.
	if err != nil || receipt.Image != "hostlier.local/artifacts@"+digest.String() {
		return "", model.Err("ARTIFACT_DIGEST", "Image manifest verification failed")
	}
	config, err := image.ConfigName()
	// Container execution identity is verified independently of the tar transport.
	if err != nil || receipt.ConfigDigest != config.String() {
		return "", model.Err("ARTIFACT_DIGEST", "Image configuration verification failed")
	}
	return digest.Hex, nil
}

// checkAvailableSpace preserves a system reserve without imposing an image byte cap.
func (s Store) checkAvailableSpace(additional int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.Root, &stat); err != nil {
		return err
	}
	if stat.Bsize <= 0 {
		return model.Err("ARTIFACT_SPACE", "Artifact filesystem capacity is unavailable")
	}
	blockSize := uint64(stat.Bsize)
	// Leave at least 256 MiB or five percent of the filesystem for other services.
	reserve := max((uint64(256<<20)+blockSize-1)/blockSize, uint64(stat.Blocks)/20)
	needed := (uint64(additional) + blockSize - 1) / blockSize
	if uint64(stat.Bavail) < reserve+needed {
		return model.Err("ARTIFACT_SPACE", "Not enough free disk space to retain the image and system reserve")
	}
	return nil
}
