package artifact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CleanupGrace lets interrupted clients resume uploads and deploy freshly committed content.
const CleanupGrace = time.Hour

var stagedArchiveRE = regexp.MustCompile(`^(?:upload-[a-f0-9]{64}-)?[a-f0-9]{64}\.tar$`)

// CleanupResult reports reclaimed store files without exposing their contents.
type CleanupResult struct {
	Files int
	Bytes int64
}

// retainedReceipt pairs verified metadata identities with their filesystem lease.
type retainedReceipt struct {
	result   Result
	name     string
	modified time.Time
}

// Sweep removes expired unreferenced content under the controller's state lock.
func (s Store) Sweep(now time.Time, protected map[string]bool) (CleanupResult, error) {
	var result CleanupResult
	entries, err := os.ReadDir(s.Root)
	// A new installation has no artifacts to reclaim.
	if os.IsNotExist(err) {
		return result, nil
	}
	// An unreadable inventory cannot authorize deletion.
	if err != nil {
		return result, err
	}
	receipts, err := s.cleanupReceipts(entries)
	// Corrupt metadata fails closed instead of guessing which archive belongs to a live image.
	if err != nil {
		return result, err
	}
	keep := map[string]bool{}
	// Shared archives remain protected while any receipt retains them.
	for _, receipt := range receipts {
		// Retained workload references take precedence over reclamation.
		if protected[receipt.result.Image] || now.Sub(receipt.modified) < CleanupGrace {
			keep[receipt.result.ArchiveDigest+".tar"] = true
			continue
		}
		// Persist image-cache cleanup intent before forgetting its authoritative receipt.
		if err = s.retireReceipt(receipt, &result); err != nil {
			return result, err
		}
	}
	// Only recognized regular staging files are eligible; volumes and unknown files are untouched.
	for _, entry := range entries {
		// Leave remaining cleanup work retryable if this operation fails.
		if err = s.removeExpiredArchive(entry, now, keep, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

// cleanupReceipts validates every managed receipt before any destructive sweep step.
func (s Store) cleanupReceipts(entries []os.DirEntry) ([]retainedReceipt, error) {
	var receipts []retainedReceipt
	// Unknown filenames and symlinks are outside garbage collection's authority.
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		// Require the bounded cleanup precondition before changing owned state.
		if !strings.HasSuffix(entry.Name(), ".json") || !digestRE.MatchString(id) {
			continue
		}
		receipt, err := s.cleanupReceipt(entry, id)
		// Leave remaining cleanup work retryable if this operation fails.
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

// cleanupReceipt validates one bounded receipt without following symlinks.
func (s Store) cleanupReceipt(entry os.DirEntry, id string) (retainedReceipt, error) {
	info, err := entry.Info()
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return retainedReceipt{}, fmt.Errorf("invalid artifact receipt metadata")
	}
	raw, err := os.ReadFile(filepath.Join(s.Root, entry.Name()))
	var receipt Result
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.Image != "hostlier.local/artifacts@sha256:"+id || !digestRE.MatchString(receipt.ArchiveDigest) {
		return retainedReceipt{}, fmt.Errorf("invalid artifact receipt identity")
	}
	return retainedReceipt{receipt, entry.Name(), info.ModTime()}, nil
}

// retireReceipt journals exact image eviction so daemon failures can be retried after restart.
func (s Store) retireReceipt(receipt retainedReceipt, result *CleanupResult) error {
	id := strings.TrimSuffix(receipt.name, ".json")
	// A fixed marker contains no caller paths or credentials.
	if err := os.WriteFile(filepath.Join(s.Root, id+".gc"), []byte(receipt.result.Image), 0600); err != nil {
		return err
	}
	return removeCleanupFile(filepath.Join(s.Root, receipt.name), result)
}

// removeExpiredArchive applies the idle lease to incomplete uploads and orphaned archive transports.
func (s Store) removeExpiredArchive(entry os.DirEntry, now time.Time, keep map[string]bool, result *CleanupResult) error {
	// Receipt temporary files are also bounded abandoned writes, never executable content.
	known := stagedArchiveRE.MatchString(entry.Name()) || strings.HasSuffix(entry.Name(), ".json.tmp") && digestRE.MatchString(strings.TrimSuffix(entry.Name(), ".json.tmp"))
	// Require the bounded cleanup precondition before changing owned state.
	if !known || keep[entry.Name()] {
		return nil
	}
	info, err := entry.Info()
	// Unknown file types are preserved; recent writes retain their resumability lease.
	if err != nil {
		return err
	}
	// Require the bounded cleanup precondition before changing owned state.
	if !info.Mode().IsRegular() || now.Sub(info.ModTime()) < CleanupGrace {
		return nil
	}
	return removeCleanupFile(filepath.Join(s.Root, entry.Name()), result)
}

// removeCleanupFile counts only confirmed removal of a regular managed file.
func removeCleanupFile(path string, result *CleanupResult) error {
	info, err := os.Lstat(path)
	// Already removed files make retries idempotent.
	if os.IsNotExist(err) {
		return nil
	}
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil {
		return err
	}
	// Never remove a path that changed into an unrelated filesystem object.
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact cleanup requires a regular file")
	}
	// Leave remaining cleanup work retryable if this operation fails.
	if err = os.Remove(path); err != nil {
		return err
	}
	result.Files++
	result.Bytes += info.Size()
	return nil
}

// PendingImages returns exact managed image identities whose archive leases have expired.
func (s Store) PendingImages() ([]string, error) {
	entries, err := os.ReadDir(s.Root)
	// Empty installations have no pending image-cache work.
	if os.IsNotExist(err) {
		return nil, nil
	}
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil {
		return nil, err
	}
	var images []string
	// Marker filenames alone determine the immutable image target.
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".gc")
		// Require the bounded cleanup precondition before changing owned state.
		if strings.HasSuffix(entry.Name(), ".gc") && digestRE.MatchString(id) && entry.Type().IsRegular() {
			images = append(images, "hostlier.local/artifacts@sha256:"+id)
		}
	}
	return images, nil
}

// ForgetImage clears a completed eviction intent, including one superseded by a new protected reference.
func (s Store) ForgetImage(image string) error {
	id := strings.TrimPrefix(image, "hostlier.local/artifacts@sha256:")
	// Only canonical artifact identities select cleanup markers.
	if !IsImage(image) || !digestRE.MatchString(id) {
		return fmt.Errorf("invalid cleanup image")
	}
	err := os.Remove(filepath.Join(s.Root, id+".gc"))
	// Leave remaining cleanup work retryable if this operation fails.
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
