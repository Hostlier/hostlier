package migration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// spacePreflight reserves room for independent source backups, target data, and
// target backups used by rollback. Declared Docker volume sizes are not quotas.
func spacePreflight(ctx context.Context, p *Plan) error {
	total, err := measureVolumes(ctx, p)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Metadata has a separate portable backup and may contain retained image blobs.
	metadata, err := measureMetadata(ctx, p.StateDir)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	backup, err := existingParent(p.BackupDir)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	var targetFS, backupFS syscall.Statfs_t
	// Preserve failure before advancing to the next external-effect boundary.
	if err := syscall.Statfs(p.StateDir, &targetFS); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := syscall.Statfs(backup, &backupFS); err != nil {
		return err
	}
	reserve := uint64(512<<20) + 2*metadata
	targetNeed, backupNeed := total+reserve, 2*total+reserve
	// Reject incompatible state before continuing the protected migration step.
	if targetFS.Fsid == backupFS.Fsid {
		targetNeed = 3*total + reserve
		backupNeed = targetNeed
	}
	targetFree := uint64(targetFS.Bavail) * uint64(targetFS.Bsize)
	backupFree := uint64(backupFS.Bavail) * uint64(backupFS.Bsize)
	// Reject incompatible state before continuing the protected migration step.
	if targetFree < targetNeed || backupFree < backupNeed {
		return fmt.Errorf("insufficient free storage: need at least %d bytes on state filesystem and %d bytes on backup filesystem, including rollback copies", targetNeed, backupNeed)
	}
	p.Changes = append(p.Changes, "Check free storage for independent source backups, target copies and rollback snapshots, with at least 512 MiB additional headroom")
	return nil
}

// measureMetadata excludes data volumes, which have independent cold archives.
func measureMetadata(ctx context.Context, dir string) (uint64, error) {
	args := []string{"-sx", "--apparent-size", "--block-size=1", "--exclude=migration-data", "--exclude=collector-image", "--exclude=observations", "--exclude=package-uploads", "--", dir}
	out, err := exec.CommandContext(ctx, "du", args...).Output()
	// Unknown storage cannot authorize a migration backup.
	if err != nil {
		return 0, fmt.Errorf("cannot measure controller metadata storage")
	}
	fields := strings.Fields(string(out))
	// A missing or oversized measurement cannot be replaced with a guess.
	if len(fields) == 0 {
		return 0, fmt.Errorf("controller metadata measurement unavailable")
	}
	size, err := strconv.ParseUint(fields[0], 10, 64)
	// Bound multiplication when reserving both metadata staging and backup bytes.
	if err != nil || size > 1<<60 {
		return 0, fmt.Errorf("controller metadata storage measurement overflow")
	}
	return size, nil
}

// measureVolumes bounds arithmetic while summing actual allocated source storage.
func measureVolumes(ctx context.Context, p *Plan) (uint64, error) {
	var total uint64
	// Process every primary and sidecar volume within the same writer fence.
	for _, v := range p.Volumes {
		size, err := measureVolume(ctx, v.Path)
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return 0, err
		}
		// Reject incompatible state before continuing the protected migration step.
		if total > (1<<60)-size {
			return 0, fmt.Errorf("source volume storage measurement overflow")
		}
		total += size
	}
	return total, nil
}

// measureVolume counts sparse allocation without trusting declared volume quotas.
func measureVolume(ctx context.Context, path string) (uint64, error) {
	out, err := exec.CommandContext(ctx, "du", "-sx", "--block-size=1", "--", path).Output()
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return 0, fmt.Errorf("cannot measure source volume storage")
	}
	fields := strings.Fields(string(out))
	// Reject incompatible state before continuing the protected migration step.
	if len(fields) == 0 {
		return 0, fmt.Errorf("cannot measure source volume storage")
	}
	size, err := strconv.ParseUint(fields[0], 10, 64)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil || size > 1<<60 {
		return 0, fmt.Errorf("source volume storage measurement overflow")
	}
	return size, nil
}

// existingParent keeps this migration step within its validated recovery boundary.
func existingParent(p string) (string, error) {
	// Bound repeated observations by completion, input exhaustion or cancellation.
	for {
		info, err := os.Stat(p)
		// Advance only after the preceding recovery check succeeds.
		if err == nil {
			// Reject incompatible state before continuing the protected migration step.
			if !info.IsDir() {
				return "", fmt.Errorf("storage ancestor must be a directory")
			}
			return p, nil
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(p)
		// Reject incompatible state before continuing the protected migration step.
		if parent == p {
			return "", err
		}
		p = parent
	}
}
