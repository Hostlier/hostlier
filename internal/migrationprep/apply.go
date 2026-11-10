package migrationprep

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// RetainedAsset identifies independent backup bytes and the original same-filesystem quarantine.
type RetainedAsset struct {
	Asset
	Archive       string `json:"archive"`
	ArchiveSHA256 string `json:"archiveSha256"`
	Quarantine    string `json:"quarantine"`
}

// Journal is durable before every host mutation and survives interrupted preparation.
type Journal struct {
	Version      int             `json:"version"`
	ID           string          `json:"id"`
	Phase        string          `json:"phase"`
	Plan         Plan            `json:"plan"`
	Assets       []RetainedAsset `json:"assets"`
	Fstab        []byte          `json:"fstab,omitempty"`
	SwapBackedUp bool            `json:"swapBackedUp"`
	Masks        []string        `json:"masks"`
}

// Apply requires explicit action approval and the exact fingerprint reviewed separately.
func Apply(ctx context.Context, p Plan, approved bool) error {
	// Approval cannot bypass blockers or replace a missing review identity.
	if !approved || p.Fingerprint == "" || fingerprint(p) != p.Fingerprint || len(p.Errors) > 0 {
		return fmt.Errorf("Preparation requires an approved, unchanged plan without blockers")
	}
	// Validate the installed source and protected ancestry before opening a host lock file.
	if err := (localHost{}).platform(p.StateDir); err != nil {
		return err
	}
	lock, err := preparationLock(p.StateDir)
	// Only one local preparation or rollback may hold the host boundary.
	if err != nil {
		return err
	}
	defer lock.Close()
	fresh, err := Inspect(ctx, p.Options)
	// Reinspect all source bytes and service state immediately before acquiring ownership.
	if err != nil {
		return err
	}
	// A different action, source or host condition requires another explicit review.
	if len(fresh.Errors) > 0 || fresh.Fingerprint != p.Fingerprint {
		return fmt.Errorf("Preparation plan changed; inspect and approve again")
	}
	return apply(ctx, p)
}

// preparationLock serializes host changes independently of the live Docker controller store.
func preparationLock(state string) (*os.File, error) {
	path := filepath.Join(state, "migration-preparation.lock")
	// Existing redirects must not let a root open truncate or follow an attacker-controlled file.
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Unsafe preparation lock")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	// A failed open cannot grant mutual exclusion.
	if err != nil {
		return nil, err
	}
	// Nonblocking acquisition avoids waiting with a stale inspection.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("Another preparation owns the host")
	}
	return f, nil
}

// apply retains complete backups before changing swap or publishing any quarantine rename.
func apply(ctx context.Context, p Plan) error {
	random := make([]byte, 12)
	// Random quarantine names never replace a previous recovery source.
	if _, err := rand.Read(random); err != nil {
		return err
	}
	j := Journal{Version: 1, ID: hex.EncodeToString(random), Phase: "backing-up", Plan: p, Assets: []RetainedAsset{}, Masks: []string{}}
	// Atomic directory creation refuses all pre-existing or linked backup locations.
	if err := os.Mkdir(p.BackupDir, 0700); err != nil {
		return err
	}
	// Persist review ownership before any external action.
	if err := save(j); err != nil {
		return err
	}
	// Mark unresolved work so init and migration cannot race later recovery.
	if err := atomicFile(marker(p.StateDir), []byte(p.BackupDir), 0600); err != nil {
		return err
	}
	// Failed or interrupted operations retain their journal for explicit rollback.
	if err := backup(ctx, &j); err != nil {
		return err
	}
	// Apply selected actions only after all independent backups are complete.
	if err := prepareActions(ctx, &j); err != nil {
		return err
	}
	j.Phase = "completed"
	// The durable completion record precedes removal of the interoperation guard.
	if err := save(j); err != nil {
		return err
	}
	return removeMarker(j)
}

// backup creates independent cold archives and captures exact persistent swap configuration.
func backup(ctx context.Context, j *Journal) error {
	// Swap configuration is retained even when currently no swap is active.
	if j.Plan.DisableSwap {
		raw, err := os.ReadFile("/etc/fstab")
		// Changed fstab bytes invalidate the reviewed persistent policy.
		if err != nil || digest(raw) != j.Plan.FstabDigest {
			return fmt.Errorf("fstab changed before preparation backup")
		}
		j.Fstab = raw
		j.SwapBackedUp = true
		// Write and sync original configuration before considering edits.
		if err := save(*j); err != nil {
			return err
		}
	}
	// Archive every reviewed asset before the first source is renamed.
	for i, a := range j.Plan.Assets {
		file := filepath.Join(j.Plan.BackupDir, fmt.Sprintf("cluster-%03d.tar", i))
		sha, err := archive(ctx, a, file)
		// Incomplete or unverified backups never authorize quarantine.
		if err != nil {
			return err
		}
		j.Assets = append(j.Assets, RetainedAsset{Asset: a, Archive: file, ArchiveSHA256: sha, Quarantine: a.Path + ".hostlier-quarantine-" + j.ID})
		// Record each verified recovery source before advancing to the next one.
		if err := save(*j); err != nil {
			return err
		}
	}
	return nil
}

// archive checks complete source digests before and after GNU tar's independent readback.
func archive(ctx context.Context, a Asset, dest string) (string, error) {
	fresh, err := (localHost{}).asset(a.Path)
	// Cold sources must still match the exact approved file tree.
	if err != nil || fresh.Digest != a.Digest {
		return "", fmt.Errorf("Cluster asset changed before backup: %s", a.Path)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	// Existing archive files are never overwritten by a retry.
	if err != nil {
		return "", err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "tar", "--acls", "--xattrs", "--numeric-owner", "--sparse", "-cp", "-C", filepath.Dir(a.Path), "--", filepath.Base(a.Path))
	cmd.Stdout = f
	// A failed tar leaves the originals untouched and the journal unresolved.
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Cold cluster backup failed")
	}
	// Complete bytes must reach stable storage before a checksum is accepted.
	if err := f.Sync(); err != nil {
		return "", err
	}
	sha, err := fileDigest(dest)
	// Read the complete independent archive back before comparing its contents.
	if err != nil {
		return "", err
	}
	cmd = exec.CommandContext(ctx, "tar", "--acls", "--xattrs", "--compare", "--file", dest, "--directory", filepath.Dir(a.Path))
	// Metadata and file contents must agree with the cold source.
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Cold cluster backup comparison failed")
	}
	fresh, err = (localHost{}).asset(a.Path)
	// A source write during backup invalidates the whole preparation.
	if err != nil || fresh.Digest != a.Digest {
		return "", fmt.Errorf("Cluster asset changed during backup")
	}
	return sha, nil
}

// prepareActions records intent before changing either selected host prerequisite.
func prepareActions(ctx context.Context, j *Journal) error {
	j.Phase = "prepared"
	// A durable backup checkpoint precedes every host action.
	if err := save(*j); err != nil {
		return err
	}
	// Swap disabling remains separately selected and reversible.
	if j.Plan.DisableSwap {
		// Failed swapoff retains original files and the recovery journal.
		if err := disableSwap(ctx, j); err != nil {
			return err
		}
	}
	// Quarantine never adopts or starts an existing cluster.
	if j.Plan.QuarantineK3s {
		return quarantine(ctx, j)
	}
	return nil
}

// disableSwap verifies dynamic headroom before applying persistence guards and per-path swapoff.
func disableSwap(ctx context.Context, j *Journal) error {
	// Recheck all current activation paths and RAM before editing persistence.
	if err := swapoffPreflight(j); err != nil {
		return err
	}
	j.Phase = "disabling-swap"
	// Intent must survive a crash before the first fstab edit.
	if err := save(*j); err != nil {
		return err
	}
	// Only reviewed swap entries are changed; original bytes remain in the journal.
	if err := replaceFstab(disabledFstab(j.Fstab)); err != nil {
		return err
	}
	// Every mask is recorded independently before creation.
	if err := maskSwapUnits(ctx, j); err != nil {
		return err
	}
	// Disable each reviewed activation without touching signatures or device contents.
	for _, s := range j.Plan.Swaps {
		// Failed swapoff leaves the original backing file available for rollback.
		if err := run(ctx, "swapoff", "--", s.Path); err != nil {
			return err
		}
	}
	// Rebuild generated swap units from the reviewed persistent policy.
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return swapDisabled(localHost{})
}

// swapoffPreflight rejects changed activations and checks volatile memory immediately before mutation.
func swapoffPreflight(j *Journal) error {
	raw, err := os.ReadFile("/proc/swaps")
	// Unknown runtime state cannot authorize swapoff.
	if err != nil {
		return err
	}
	swaps, used, err := parseSwaps(raw)
	// Every mapping must remain readable and unambiguous.
	if err != nil {
		return err
	}
	// Compare paths and priorities without including volatile page counts.
	if !sameSwaps(swaps, j.Plan.Swaps) {
		return fmt.Errorf("Active swap changed after preparation review")
	}
	// Memory reserve is evaluated at the execution boundary.
	if err := memoryHeadroom(localHost{}, used); err != nil {
		return err
	}
	current, err := os.ReadFile("/etc/fstab")
	// Concurrent persistent policy edits require a new review.
	if err != nil || digest(current) != j.Plan.FstabDigest {
		return fmt.Errorf("fstab changed after review")
	}
	return nil
}

// sameSwaps compares precisely the activation parameters which preparation will change.
func sameSwaps(a, b []Swap) bool {
	// Any new or removed mapping changes the approved scope.
	if len(a) != len(b) {
		return false
	}
	// Unit identifiers are derived independently; compare kernel paths and priorities.
	for i := range a {
		// An administrator's changed priority is also a new host policy.
		if a[i].Path != b[i].Path || a[i].Priority != b[i].Priority {
			return false
		}
	}
	return true
}

// maskSwapUnits retains per-unit intent and never replaces an existing mask or unit definition.
func maskSwapUnits(ctx context.Context, j *Journal) error {
	// Each mask has a separately durable recovery record.
	for _, unit := range j.Plan.SwapUnits {
		// Do not adopt a unit file or mask created after the review.
		if _, err := os.Lstat(filepath.Join("/etc/systemd/system", unit)); !os.IsNotExist(err) {
			return fmt.Errorf("Swap unit override appeared after review")
		}
		j.Masks = append(j.Masks, unit)
		// Intent before mask creation supports recovery from either side of a crash.
		if err := save(*j); err != nil {
			return err
		}
		// --now is absent so only the exact reviewed paths are passed to swapoff.
		if err := run(ctx, "systemctl", "mask", unit); err != nil {
			return err
		}
	}
	return nil
}

// swapDisabled requires a successful kernel read and no remaining mappings.
func swapDisabled(h host) error {
	raw, err := h.read("/proc/swaps")
	// Inspection failure is not evidence that swapoff succeeded.
	if err != nil {
		return err
	}
	swaps, _, err := parseSwaps(raw)
	// Any unexpected mapping keeps preparation unresolved.
	if err != nil || len(swaps) > 0 {
		return fmt.Errorf("Host swap remains active; inspect preparation before continuing")
	}
	return nil
}

// quarantine moves only the fully reviewed, backed-up standard assets after a fresh cold-state check.
func quarantine(ctx context.Context, j *Journal) error {
	// An external start after backup cannot authorize moving its live files.
	if err := clusterIdle(ctx, localHost{}); err != nil {
		return err
	}
	j.Phase = "quarantining"
	// Persist all source/destination mappings before any rename.
	if err := save(*j); err != nil {
		return err
	} // Persist all source/destination mappings before any rename.
	// Same-filesystem renames retain originals with their complete metadata.
	for _, a := range j.Assets {
		// Revalidate and move exactly one retained source at a time.
		if err := quarantineAsset(a); err != nil {
			return err
		}
	}
	return run(ctx, "systemctl", "daemon-reload")
}

// quarantineAsset verifies both independent backup and source before a durable same-filesystem rename.
func quarantineAsset(a RetainedAsset) error {
	fresh, err := (localHost{}).asset(a.Path)
	// Only the reviewed source tree may cross the rename boundary.
	if err != nil || fresh.Digest != a.Digest {
		return fmt.Errorf("Cluster asset changed before quarantine")
	}
	// Unknown or existing destinations cannot be replaced.
	if _, err := os.Lstat(a.Quarantine); !os.IsNotExist(err) {
		return fmt.Errorf("Quarantine destination already exists")
	}
	// The independent archive must still match its journal before releasing the source name.
	if sha, err := fileDigest(a.Archive); err != nil || sha != a.ArchiveSHA256 {
		return fmt.Errorf("Cluster backup integrity check failed")
	}
	// Rename rather than delete or restore into a shared parent tree.
	if err := os.Rename(a.Path, a.Quarantine); err != nil {
		return err
	}
	// The rename must be durable before another cluster asset is moved.
	if err := syncDirectory(filepath.Dir(a.Path)); err != nil {
		return err
	}
	return nil
}

// Read loads the retained journal without changing host state.
func Read(dir string) (Journal, error) {
	var j Journal
	// Validate ancestry before reading privileged recovery metadata.
	if err := protectedParent(dir); err != nil {
		return j, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "preparation.json"))
	// Unknown state cannot authorize recovery.
	if err != nil {
		return j, err
	}
	err = json.Unmarshal(raw, &j)
	// Only the supported, self-consistent journal format may select recovery paths.
	if err != nil || j.Version != 1 || j.Plan.BackupDir != dir || fingerprint(j.Plan) != j.Plan.Fingerprint {
		return j, fmt.Errorf("Invalid preparation journal")
	}
	return j, nil
}

// save commits every recovery checkpoint privately and synchronizes its containing directory.
func save(j Journal) error {
	raw, err := json.MarshalIndent(j, "", "  ")
	// Serialization must complete before publication.
	if err != nil {
		return err
	}
	return atomicFile(filepath.Join(j.Plan.BackupDir, "preparation.json"), raw, 0600)
} // Serialization must complete before publication.

// atomicFile refuses symlinks and privately stages a fully synced replacement.
func atomicFile(path string, raw []byte, mode os.FileMode) error {
	// Existing nonregular targets are outside the recovery contract.
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("Unsafe preparation metadata target")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".preparation-")
	// No destination is changed if private staging cannot be created.
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	// Apply private permissions before writing protected configuration bytes.
	if err := f.Chmod(mode); err != nil {
		return err
	}
	// A partial write cannot replace the original record.
	if _, err := f.Write(raw); err != nil {
		return err
	}
	// The replacement must be durable before it receives the public filename.
	if err := f.Sync(); err != nil {
		return err
	}
	// Atomic rename preserves either the previous or the complete new record.
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

// replaceFstab preserves the host file's existing mode and rejects symlinks.
func replaceFstab(raw []byte) error {
	info, err := os.Lstat("/etc/fstab")
	// fstab ownership and redirection are not adopted by preparation.
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("fstab must be a regular file")
	}
	return atomicFile("/etc/fstab", raw, info.Mode().Perm())
}

// fileDigest reads every retained archive byte without interpreting archive paths.
func fileDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	// Recovery never follows an archive symlink.
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("Preparation archive must be a regular file")
	}
	f, err := os.Open(path)
	// A missing archive cannot authorize renames or rollback.
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}

// syncDirectory makes rename and marker-removal boundaries durable across reboot.
func syncDirectory(path string) error {
	f, err := os.Open(path)
	// Opening failure prevents confirming durability.
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
} // Opening failure prevents confirming durability.

// marker is separate from the migration startup guard because Docker stays available during preparation.
func marker(state string) string { return filepath.Join(state, "migration-preparation-active") }

// removeMarker releases only this journal's interoperation reservation.
func removeMarker(j Journal) error {
	raw, err := os.ReadFile(marker(j.Plan.StateDir))
	// A completed rollback may have already released its marker.
	if os.IsNotExist(err) {
		return nil
	}
	// Another journal's ownership must never be removed.
	if err != nil || strings.TrimSpace(string(raw)) != j.Plan.BackupDir {
		return fmt.Errorf("Preparation ownership marker changed")
	}
	// Flush marker removal before reporting a fully resolved preparation.
	if err := os.Remove(marker(j.Plan.StateDir)); err != nil {
		return err
	}
	return syncDirectory(j.Plan.StateDir)
}

// run executes a fixed-purpose tool and withholds protected subprocess output from public diagnostics.
func run(ctx context.Context, file string, args ...string) error {
	// Failed external effects retain their recovery intent.
	if err := exec.CommandContext(ctx, file, args...).Run(); err != nil {
		return fmt.Errorf("Preparation %s failed; inspect retained journal: %w", file, err)
	}
	return nil
} // Failed external effects retain their recovery intent.
