package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hostlier/internal/model"
)

// ChunkBytes fits a base64-encoded chunk within the controller's 2 MiB request limit.
const ChunkBytes = 1024 * 1024

// LegacyChunkBytes preserves compatibility with controllers without status negotiation.
const LegacyChunkBytes = 65536

// UploadStatus advertises a durable prefix or independently verified committed content.
type UploadStatus struct {
	ChunkBytes   int    `json:"chunkBytes"`
	Offset       int64  `json:"offset"`
	PrefixDigest string `json:"prefixDigest"`
	Committed    bool   `json:"committed"`
}

// ValidReceipt bounds caller identities before they select cache or controller files.
func ValidReceipt(r Result) bool {
	return digestRE.MatchString(r.ArchiveDigest) && r.Size > 0 &&
		IsImage(r.Image) && digestRE.MatchString(strings.TrimPrefix(r.Image, "hostlier.local/artifacts@sha256:")) &&
		strings.HasPrefix(r.ConfigDigest, "sha256:") && digestRE.MatchString(strings.TrimPrefix(r.ConfigDigest, "sha256:"))
}

// Status verifies shared committed content or hashes only this uploader's retained prefix.
func (s Store) Status(receipt Result) (UploadStatus, error) {
	status := UploadStatus{ChunkBytes: ChunkBytes}
	// Arbitrary names and impossible lengths must not reach filesystem lookups.
	if !ValidReceipt(receipt) {
		return status, model.Err("ARTIFACT_DIGEST", "Invalid artifact receipt")
	}
	committed, err := s.Inspect(receipt.Image)
	// Only a complete match may skip transport and commit verification.
	if err == nil && sameReceipt(committed, receipt) {
		return s.committedStatus(committed)
	}
	var failure *model.Error
	// Missing content is a cache miss; corruption or I/O errors remain visible.
	if err != nil && !(errors.As(err, &failure) && failure.Code == "ARTIFACT_NOT_FOUND") {
		return status, err
	}
	path, err := s.chunkPath(receipt.ArchiveDigest)
	// Staging paths remain isolated by the authenticated upload owner.
	if err != nil {
		return status, err
	}
	status.Offset, status.PrefixDigest, err = durablePrefix(path, receipt.Size)
	return status, err
}

// committedStatus verifies runnable metadata as well as archive bytes before returning a cache hit.
func (s Store) committedStatus(receipt Result) (UploadStatus, error) {
	status := UploadStatus{ChunkBytes: ChunkBytes}
	// Retained receipt metadata alone cannot prove that the archive contains the claimed image.
	if _, err := verifyImageReceipt(receipt.Path, receipt); err != nil {
		return status, err
	}
	status.Offset, status.Committed = receipt.Size, true
	return status, s.touchReceipt(receipt)
}

// sameReceipt compares every identity required to reuse a prepared image.
func sameReceipt(a, b Result) bool {
	return a.Image == b.Image && a.ConfigDigest == b.ConfigDigest && a.ArchiveDigest == b.ArchiveDigest && a.Size == b.Size
}

// touchReceipt renews the preparation lease without changing retained image bytes.
func (s Store) touchReceipt(receipt Result) error {
	id := strings.TrimPrefix(receipt.Image, "hostlier.local/artifacts@sha256:")
	return os.Chtimes(filepath.Join(s.Root, id+".json"), time.Now(), time.Now())
}

// durablePrefix syncs interrupted writes before advertising them as resumable progress.
func durablePrefix(path string, limit int64) (int64, string, error) {
	info, err := os.Lstat(path)
	// No retained upload means the caller starts at zero.
	if os.IsNotExist(err) {
		return 0, "", nil
	}
	// Nonregular or oversized staging cannot be trusted as a resumable prefix.
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return 0, "", model.Err("ARTIFACT_OFFSET", "Invalid retained upload prefix")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	// An unreadable prefix cannot become an acknowledgement.
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	// A status response may acknowledge bytes written before a process interruption.
	if err = f.Sync(); err != nil {
		return 0, "", err
	}
	digest, err := PrefixDigest(f, info.Size())
	// Active resume queries renew the upload's idle lease.
	if err == nil {
		err = os.Chtimes(path, time.Now(), time.Now())
	}
	return info.Size(), digest, err
}

// PrefixDigest hashes exactly the requested prefix and leaves the reader ready to resume.
func PrefixDigest(r io.Reader, size int64) (string, error) {
	hash := sha256.New()
	_, err := io.CopyN(hash, r, size)
	return hex.EncodeToString(hash.Sum(nil)), err
}
