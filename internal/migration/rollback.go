package migration

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"hostlier/internal/controller"
	"hostlier/internal/store"
)

// Read reports the durable phase without contacting either runtime.
func Read(stateDir string) (*Journal, error) {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := safeAncestors(stateDir); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, "migration.json")
	info, err := os.Lstat(path)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	// Reject incompatible state before continuing the protected migration step.
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<20 {
		return nil, fmt.Errorf("unsafe migration journal")
	}
	raw, err := os.ReadFile(path)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	return decodeJournal(raw, stateDir)
}

// decodeJournal validates the recovery envelope before any path or runtime is trusted.
func decodeJournal(raw []byte, stateDir string) (*Journal, error) {
	var j Journal
	// Validate the decoded native response before using its contents.
	if json.Unmarshal(raw, &j) != nil || !journalIdentityValid(&j, stateDir) {
		return nil, fmt.Errorf("invalid migration journal")
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := validateOptions(j.Plan.Options); err != nil {
		return nil, err
	}
	// Select only representations explicitly supported by the migration contract.
	switch j.Phase {
	// Keep this alternative within the validated operation or archive contract.
	case "prepared", "backing-up", "backed-up", "restoring", "committing", "completed", "rolling-back", "rolled-back":
	// Keep this alternative within the validated operation or archive contract.
	default:
		return nil, fmt.Errorf("unknown migration phase")
	}
	return &j, nil
}

// journalIdentityValid confines recovery to this version and original state directory.
func journalIdentityValid(j *Journal, stateDir string) bool {
	_, err := hex.DecodeString(j.ID)
	return err == nil && j.Version == 1 && j.Plan.StateDir == stateDir && len(j.ID) == 32 && journalCollectionsPresent(j)
}

// journalCollectionsPresent distinguishes a complete empty inventory from missing recovery data.
func journalCollectionsPresent(j *Journal) bool {
	return j.Policies != nil && j.Backups != nil && j.TargetBackups != nil && j.Specifications != nil
}

// CheckStartup also protects direct daemon invocations outside systemd.
func CheckStartup(stateDir string) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if _, err := os.Lstat(filepath.Join(stateDir, "migration-active")); err == nil {
		return fmt.Errorf("runtime migration is active; inspect migrate status and run migrate rollback before starting the controller")
		// Preserve failure before advancing to the next external-effect boundary.
	} else if !os.IsNotExist(err) {
		return err
	}
	j, err := Read(stateDir)
	// Preserve failure before advancing to the next external-effect boundary.
	if os.IsNotExist(err) {
		return nil
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Only a resolved phase can release the migration startup guard.
	if j.Phase != "completed" && j.Phase != "rolled-back" {
		return fmt.Errorf("runtime migration is unresolved (%s); run migrate rollback", j.Phase)
	}
	return nil
}

// Rollback fences both sides and preserves current target data before restarting
// the original Docker groups. Later deployment changes require a new review.
func Rollback(ctx context.Context, stateDir string, approved bool, progress func(string)) error {
	// Reject incompatible state before continuing the protected migration step.
	if !approved {
		return fmt.Errorf("rollback requires explicit approval")
	}
	j, err := Read(stateDir)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	e, err := localEngine(j.Plan.Options, j.Plan.Installation)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	e.progress = progress
	st, state, current, err := e.openRollback(ctx, stateDir)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	defer st.Close()
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.rollback(ctx, current, st, state); err != nil {
		return err
	}
	st.Close()
	return e.host.service(ctx, "start")
}

// openRollback rereads recovery state only after excluding the controller writer.
func (e *engine) openRollback(ctx context.Context, stateDir string) (*store.Store, *controller.State, *Journal, error) {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.service(ctx, "stop"); err != nil {
		return nil, nil, nil, err
	}
	state := &controller.State{}
	st, err := store.Open(stateDir, state)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, nil, nil, err
	}
	j, err := Read(stateDir)
	// Advance only after the preceding recovery check succeeds.
	if err == nil {
		err = rollbackCompatible(j, *state)
	}
	// Advance only after the preceding recovery check succeeds.
	if err == nil {
		err = e.host.guard(true)
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		st.Close()
		return nil, nil, nil, err
	}
	return st, state, j, nil
}

// rollbackCompatible refuses changed workloads, credentials and unsettled operations.
func rollbackCompatible(j *Journal, state controller.State) error {
	// Reject incompatible state before continuing the protected migration step.
	if len(state.Applications) != len(j.Source) {
		return fmt.Errorf("application inventory changed since migration; review a separate data transfer before rollback")
	}
	// Complete every selected item before advancing the migration boundary.
	for name, digest := range j.Specifications {
		app := state.Applications[name]
		// Treat absent optional state explicitly rather than inferring authority.
		if app == nil || applicationSignature(app) != digest {
			return fmt.Errorf("%s: application specification or pinned secrets changed since migration; reconcile them before rollback", name)
		}
	}
	// Reject incompatible state before continuing the protected migration step.
	if unsettledOperations(state) {
		return fmt.Errorf("resolve unfinished operations before rollback")
	}

	// Reject incompatible state before continuing the protected migration step.
	if len(state.Resources) > 0 || len(state.Hosts) > 0 || len(state.Tunnels) > 0 {
		return fmt.Errorf("resources, fleet hosts or tunnels changed; review them before rollback")
	}
	return nil
}

// unsettledOperations includes interrupted work whose external outcome is unknown.
func unsettledOperations(state controller.State) bool {
	// Complete every selected item before advancing the migration boundary.
	for _, op := range state.Operations {
		// Treat absent optional state explicitly rather than inferring authority.
		if op != nil && !operationTerminal(op.Status) {
			return true
		}
	}
	return false
}

// rollback restores serving capacity only after the opposite runtime is fenced.
func (e *engine) rollback(ctx context.Context, j *Journal, st *store.Store, state *controller.State) error {
	// Only a resolved phase can release the migration startup guard.
	if j.Phase == "rolled-back" {
		return e.host.guard(false)
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := rollbackCompatible(j, *state); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.phase(j, "rolling-back"); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.fenceForRollback(ctx, j); err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if j.PreserveTarget && !j.RollbackCopied {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := e.copyCurrentTarget(ctx, j); err != nil {
			return err
		}
	}
	return e.commitSource(ctx, j, st, state)
}

// fenceForRollback also handles a source group partially started by a previous attempt.
func (e *engine) fenceForRollback(ctx context.Context, j *Journal) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.sourceFence(ctx, j, func() error { return e.save(j) }); err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if j.TargetTouched {
		return e.host.targetFence(ctx, j)
	}
	return nil
}

// copyCurrentTarget completes every snapshot before overwriting an original volume.
func (e *engine) copyCurrentTarget(ctx context.Context, j *Journal) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.backupTargetVolumes(ctx, j); err != nil {
		return err
	}
	// Process every primary and sidecar volume within the same writer fence.
	for _, v := range j.Plan.Volumes {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := e.restore(ctx, j.TargetBackups[v.Name], v.Path, true); err != nil {
			return err
		}
	}
	j.RollbackCopied = true
	return e.save(j)
}

// backupTargetVolumes reuses only complete, read-back-verified prior snapshots.
func (e *engine) backupTargetVolumes(ctx context.Context, j *Journal) error {
	// Process every primary and sidecar volume within the same writer fence.
	for _, v := range j.Plan.Volumes {
		// Reject incompatible state before continuing the protected migration step.
		if a, ok := j.TargetBackups[v.Name]; ok {
			// Preserve failure before advancing to the next external-effect boundary.
			if err := verifyArchive(a); err != nil {
				return err
			}
			continue
		}
		file, err := newArchivePath(j.Plan.BackupDir, "k3s-"+digestJSON(v.Name)[:16])
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		a, err := e.backup(ctx, e.host.targetPath(j, v), file)
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		j.TargetBackups[v.Name] = a
		// Preserve failure before advancing to the next external-effect boundary.
		if err := e.save(j); err != nil {
			return err
		}
	}
	return nil
}

// commitSource preserves later metadata while restoring original Docker runtime identities.
func (e *engine) commitSource(ctx context.Context, j *Journal, st *store.Store, state *controller.State) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.setRuntime(ctx, j, "docker"); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.restoreSource(ctx, j); err != nil {
		return err
	}
	// Complete every selected item before advancing the migration boundary.
	for name, deployment := range j.Source {
		state.Applications[name].Deployment = deployment
	}
	state.Runtime = "docker"
	state.Generation++
	// Preserve failure before advancing to the next external-effect boundary.
	if err := st.Save(state); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.phase(j, "rolled-back"); err != nil {
		return err
	}
	return e.host.guard(false)
}
