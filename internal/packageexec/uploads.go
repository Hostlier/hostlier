package packageexec

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const uploadQuota = 512 << 20
const uploadCount = 128

// Uploads retains nonexecutable, actor-owned bytes; callers serialize writes under the controller lock.
type Uploads struct{ Root string }

// path scopes content identity to its authenticated uploader and rejects redirected paths.
func (s Uploads) path(actor, digest string) (string, error) {
	// Caller-controlled content identities must never become paths.
	if actor == "" || !ValidDigest(digest) {
		return "", fmt.Errorf("invalid package upload")
	}
	// Staging uses the same protected-directory ancestry as installed executables.
	if err := prepareDirectory(s.Root); err != nil {
		return "", err
	}
	path := filepath.Join(s.Root, Digest([]byte(actor))+"-"+digest)
	info, err := os.Lstat(path)
	// An absent target is safe to create under the protected parent.
	if os.IsNotExist(err) {
		return path, nil
	}
	// Existing uploads must remain private ordinary nonexecutable files.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", fmt.Errorf("unsafe package upload")
	}
	return path, nil
}

// Chunk accepts exact byte replays while rejecting gaps, substitutions and unbounded staging.
func (s Uploads) Chunk(actor, digest string, offset int64, data []byte) (int64, error) {
	// Bound arithmetic before opening or extending a file.
	if len(data) == 0 || len(data) > ChunkBytes || offset < 0 || offset > MaxBinaryBytes-int64(len(data)) {
		return 0, fmt.Errorf("invalid package chunk")
	}
	path, err := s.path(actor, digest)
	// Identity validation must precede disk access.
	if err != nil {
		return 0, err
	}
	// Quotas include every authenticated uploader and expired files are removed lazily.
	if err = s.quota(path, offset+int64(len(data))); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	// Never acknowledge an upload that could not be opened.
	if err != nil {
		return 0, err
	}
	defer file.Close()
	end, err := appendPackageChunk(file, offset, data)
	// Identical retries renew the same lease as newly appended data.
	if err == nil {
		now := time.Now()
		err = os.Chtimes(path, now, now)
	}
	return end, err
}

// appendPackageChunk preserves the retained prefix across lost acknowledgements.
func appendPackageChunk(file *os.File, offset int64, data []byte) (int64, error) {
	info, err := file.Stat()
	// Persisted size is the sole authoritative upload cursor.
	if err != nil {
		return 0, err
	}
	// Retried chunks may acknowledge an already retained identical prefix.
	if offset < info.Size() {
		old := make([]byte, len(data))
		_, err = file.ReadAt(old, offset)
		// A changed replay cannot overwrite reviewed content.
		if err != nil || !bytes.Equal(old, data) {
			return 0, fmt.Errorf("package upload differs from retained bytes")
		}
		return info.Size(), nil
	}
	// Reject sparse or reordered uploads.
	if offset != info.Size() {
		return 0, fmt.Errorf("package upload offset mismatch")
	}
	_, err = file.WriteAt(data, offset)
	// Durable acknowledgement requires flushing the appended bytes.
	if err == nil {
		err = file.Sync()
	}
	return offset + int64(len(data)), err
}

// quota bounds abandoned staging across all actors without deleting installed packages.
func (s Uploads) quota(current string, end int64) error {
	entries, err := os.ReadDir(s.Root)
	// An unreadable quota cannot authorize more writes.
	if err != nil {
		return err
	}
	total := end
	count := 1
	// Only private ordinary upload files may participate in retained staging.
	for _, entry := range entries {
		info, err := entry.Info()
		// Unexpected files fail closed instead of escaping accounting.
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe package staging")
		}
		path := filepath.Join(s.Root, entry.Name())
		// A day exceeds approval lifetime and releases abandoned-upload quota.
		if expiredUpload(path, current, info) {
			// Failed cleanup must remain accounted for instead of granting extra quota.
			if err := os.Remove(path); err != nil {
				return err
			}
			continue
		}
		// Replays and continuations reserve only the resulting size of the current file.
		if path == current {
			total += max(info.Size(), end) - end
			continue
		}
		total += info.Size()
		count++
	}
	// The bounded count also caps metadata and directory scans.
	if total > uploadQuota || count > uploadCount {
		return fmt.Errorf("package upload quota exceeded; retry after abandoned uploads expire")
	}
	return nil
}

// Read verifies the exact actor-owned bytes at preview and again immediately before installation.
func (s Uploads) Read(actor string, request InstallRequest) ([]byte, error) {
	// Review metadata cannot select invalid or oversized content.
	if err := request.Validate(); err != nil {
		return nil, err
	}
	path, err := s.path(actor, request.Digest)
	// A different actor must upload their own bytes before asking for installation.
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	// Missing staging is never substituted with installed content.
	if err != nil {
		return nil, fmt.Errorf("package upload missing")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxBinaryBytes+1))
	// Hash and size are both bound into the approved request.
	if err != nil || int64(len(data)) != request.Size || Digest(data) != request.Digest {
		return nil, fmt.Errorf("package upload checksum or size mismatch")
	}
	// A verified approval preview keeps its upload available throughout the approval window.
	now := time.Now()
	// Do not claim a renewable preview when its lease could not be persisted.
	if err = os.Chtimes(path, now, now); err != nil {
		return nil, err
	}
	return data, nil
}

// Remove discards only the requesting actor's successfully installed staging artifact.
func (s Uploads) Remove(actor, digest string) {
	path, err := s.path(actor, digest)
	// Never remove outside this actor's validated staging identity.
	if err == nil {
		os.Remove(path)
	}
}

// expiredUpload preserves the active transfer while expiring abandoned uploads after a day.
func expiredUpload(path, current string, info os.FileInfo) bool {
	return path != current && knownUpload(info.Name()) && info.Mode().Perm() == 0600 && time.Since(info.ModTime()) > UploadGrace
}
