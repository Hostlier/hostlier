package artifact

import (
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var registryPreparationDir = regexp.MustCompile(`^registry-pull-[0-9]+$`)

// SweepPreparations removes crash-abandoned private download directories when the controller has no active pulls.
func (s Store) SweepPreparations(now time.Time) (CleanupResult, error) {
	var result CleanupResult
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) { // A new controller has no abandoned downloads.
		return result, nil
	}
	if err != nil { // Unreadable directories cannot authorize guessed cleanup paths.
		return result, err
	}
	for _, entry := range entries { // Only server-generated preparation directory names belong to this cleanup path.
		if !entry.IsDir() || !registryPreparationDir.MatchString(entry.Name()) { // Unknown directories and symlinks remain untouched.
			continue
		}
		info, err := entry.Info()
		if err != nil { // A failed metadata read cannot establish the cleanup lease.
			return result, err
		}
		if now.Sub(info.ModTime()) < CleanupGrace { // Give interrupted preparation a full cleanup grace period.
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.Root, entry.Name())); err != nil { // Remove only this private abandoned preparation tree.
			return result, err
		}
		result.Files++
	}
	return result, nil
}
