package migrationprep

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

// Rollback restores original prerequisites only when no replacement or foreign edits exist.
func Rollback(ctx context.Context, dir string, approved bool) error {
	// Host restoration is a separate explicit decision, never part of a preview.
	if !approved || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return fmt.Errorf("Preparation rollback requires explicit root approval on Linux")
	}
	j, err := Read(dir)
	// Recovery requires the exact original journal and immutable review.
	if err != nil {
		return err
	}
	lock, err := preparationLock(j.Plan.StateDir)
	// Only one local host transition may hold recovery ownership.
	if err != nil {
		return err
	}
	defer lock.Close()
	// Repeating a finished rollback performs no external effects.
	if j.Phase == "rolled-back" {
		return nil
	}
	// Validate every destination and backup before restoring the first source.
	if err := rollbackPreflight(ctx, j); err != nil {
		return err
	}
	return restorePreparation(ctx, &j)
}

// restorePreparation performs only prevalidated recovery actions and retains every backup.
func restorePreparation(ctx context.Context, j *Journal) error {
	j.Phase = "rolling-back"
	// Persist recovery intent before touching either host prerequisite.
	if err := save(*j); err != nil {
		return err
	} // Persist recovery intent before touching either host prerequisite.
	// Restore all retained cluster files before restoring host swap policy.
	if err := restoreAssets(*j); err != nil {
		return err
	}
	// Swap persistence changes have their own precise original-byte backup.
	if j.SwapBackedUp {
		// Restore only actions recorded before their mutation boundary.
		if err := rollbackSwap(ctx, *j); err != nil {
			return err
		}
	} // Restore only actions recorded before their mutation boundary.
	// Reload definitions without starting or enabling the external Kubernetes cluster.
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	j.Phase = "rolled-back"
	// Preserve the journal and all independent archives after successful recovery.
	if err := save(*j); err != nil {
		return err
	}
	return removeMarker(*j)
}

// restoreAssets reverses quarantine without ever replacing a destination.
func restoreAssets(j Journal) error {
	// Reverse the order of the original quarantine renames.
	for i := len(j.Assets) - 1; i >= 0; i-- {
		a := j.Assets[i]
		// An interrupted backup may never have moved this source.
		if _, err := os.Lstat(a.Quarantine); os.IsNotExist(err) {
			continue
		}
		// No restored original may overwrite an independently created replacement.
		if err := os.Rename(a.Quarantine, a.Path); err != nil {
			return err
		}
		// Make each rename durable before continuing partial recovery.
		if err := syncDirectory(filepath.Dir(a.Path)); err != nil {
			return err
		}
	}
	return nil
}

// rollbackPreflight refuses running clusters, altered quarantines, corrupt backups and replacement paths.
func rollbackPreflight(ctx context.Context, j Journal) error {
	// Journal identities are confined before deriving a same-filesystem recovery destination.
	if !regexp.MustCompile(`^[a-f0-9]{24}$`).MatchString(j.ID) {
		return fmt.Errorf("Invalid preparation identity")
	}
	// Original cluster assets cannot be restored beneath live target writers.
	if j.Plan.QuarantineK3s {
		// Completed runtime migration must be rolled back independently first.
		if err := clusterIdle(ctx, localHost{}); err != nil {
			return err
		}
	} // Completed runtime migration must be rolled back independently first.
	// Validate every move before performing any restoration.
	for i, a := range j.Assets {
		// A corrupt record cannot expand the reviewed recovery scope.
		if err := validateAssetRecord(j, i, a); err != nil {
			return err
		}
		// Full archive readback catches storage corruption before releasing quarantine.
		if sha, err := fileDigest(a.Archive); err != nil || sha != a.ArchiveSHA256 {
			return fmt.Errorf("Preparation backup checksum mismatch")
		}
		// An unchanged original or an unchanged quarantine is the only recoverable arrangement.
		if err := restorableAsset(a); err != nil {
			return err
		}
	}
	return validateRollbackSwap(j)
}

// validateAssetRecord binds every recovery path to its original reviewed order.
func validateAssetRecord(j Journal, i int, a RetainedAsset) error {
	// A corrupted journal cannot select paths outside its exact approved asset order.
	if i >= len(j.Plan.Assets) || a.Asset != j.Plan.Assets[i] || a.Quarantine != a.Path+".hostlier-quarantine-"+j.ID || a.Archive != filepath.Join(j.Plan.BackupDir, fmt.Sprintf("cluster-%03d.tar", i)) {
		return fmt.Errorf("Invalid preparation asset record")
	}
	return nil
}

// restorableAsset detects conflicting replacements before any original source is renamed back.
func restorableAsset(a RetainedAsset) error {
	original, oe := (localHost{}).asset(a.Path)
	retained, re := (localHost{}).asset(a.Quarantine)
	// An unreadable tree cannot authorize replacing or abandoning either side.
	if oe != nil || re != nil {
		return fmt.Errorf("Cannot verify preparation recovery assets")
	}
	// Already-restored sources support recovery after a partially completed rollback.
	if retained.Digest == "" && original.Digest == a.Digest {
		return nil
	}
	// A new source path or changed quarantine requires manual reconciliation.
	if original.Digest != "" || retained.Digest != a.Digest {
		return fmt.Errorf("Preparation rollback refuses changed or replacement cluster assets: %s", a.Path)
	}
	return nil
}

// validateRollbackSwap protects foreign configuration edits and preparation-owned unit masks.
func validateRollbackSwap(j Journal) error {
	// An interrupted cluster-only backup has no swap state to restore.
	if !j.SwapBackedUp {
		return nil
	}
	raw, err := os.ReadFile("/etc/fstab")
	// Preserve unrelated administrator changes made after preparation.
	if err != nil || digest(j.Fstab) != j.Plan.FstabDigest || (digest(raw) != j.Plan.FstabDigest && digest(raw) != digest(disabledFstab(j.Fstab))) {
		return fmt.Errorf("fstab changed after preparation; reconcile it before rollback")
	}
	return validateMasks(j)
}

// validateMasks refuses foreign or changed overrides before rollback removes any mask.
func validateMasks(j Journal) error {
	// Never remove a foreign or changed unit definition under the guise of unmasking.
	for i, unit := range j.Masks {
		// Recorded mask intent must be a prefix of the exact reviewed mask list.
		if i >= len(j.Plan.SwapUnits) || unit != j.Plan.SwapUnits[i] {
			return fmt.Errorf("Invalid swap mask recovery record")
		}
		target, err := os.Readlink(filepath.Join("/etc/systemd/system", unit))
		// A crash before mask creation is safe to recover without unmasking anything.
		if os.IsNotExist(err) {
			continue
		}
		// Changed unit content or non-null links are never deleted automatically.
		if err != nil || target != "/dev/null" {
			return fmt.Errorf("Swap unit mask changed after preparation")
		}
	}
	return nil
}

// rollbackSwap restores only the prior persistence policy and activations, without recreating swap storage.
func rollbackSwap(ctx context.Context, j Journal) error {
	// Preserve the original bytes and mode before removing our persistent masks.
	if err := replaceFstab(j.Fstab); err != nil {
		return err
	}
	// Only journaled, still-null masks are eligible for removal.
	for _, unit := range j.Masks {
		path := filepath.Join("/etc/systemd/system", unit)
		// Missing masks permit safe resumption after an interrupted rollback.
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		// Remove exactly this mask rather than asking systemctl to remove additional runtime masks.
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	// Sync mask removal before regenerating fstab units.
	if err := syncDirectory("/etc/systemd/system"); err != nil {
		return err
	}
	// Regenerate exact original persistent configuration before restoring active mappings.
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return restoreSwapActivations(ctx, j)
}

// restoreSwapActivations resumes only original active mappings while leaving existing ones untouched.
func restoreSwapActivations(ctx context.Context, j Journal) error {
	raw, err := os.ReadFile("/proc/swaps")
	// Current activations must be known before considering any repeated swapon.
	if err != nil {
		return err
	}
	active, _, err := parseSwaps(raw)
	// Ambiguous activation paths cannot be safely reconciled automatically.
	if err != nil {
		return err
	}
	known := map[string]bool{}
	// Already restored mappings remain untouched during a repeated rollback.
	for _, s := range active {
		known[s.Path] = true
	}
	// Swap files and signatures were retained, so no mkswap or formatting is required.
	for _, s := range j.Plan.Swaps {
		// Preserve existing activations rather than changing their runtime parameters.
		if known[s.Path] {
			continue
		}
		args := []string{}
		// Negative kernel-assigned priorities are restored by the kernel, not passed to --priority.
		if len(s.Priority) > 0 && s.Priority[0] != '-' {
			args = append(args, "--priority", s.Priority)
		}
		args = append(args, "--", s.Path)
		// A failed activation leaves the journal recoverable and does not start Kubernetes.
		if err := run(ctx, "swapon", args...); err != nil {
			return err
		}
	}
	return nil
}
