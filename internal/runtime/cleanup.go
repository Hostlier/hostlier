package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// failedGroupCleanup retains only exact owned container identities from a failed attempt.
type failedGroupCleanup struct {
	Network    string   `json:"network,omitempty"`
	App        string   `json:"app"`
	Containers []string `json:"containers"`
}

// failedGroup identifies candidate members before their primary and namespace owner.
func (d *dockerDeployment) failedGroup() failedGroupCleanup {
	job := failedGroupCleanup{App: d.spec.Name, Network: d.network}
	// Source members never become cleanup targets during an idempotent replay.
	for name, id := range d.members {
		// Require the bounded cleanup precondition before changing owned state.
		if id != "" && id != d.old.Members[name] {
			job.Containers = append(job.Containers, id)
		}
	}
	// A missing primary must not hide an already-created network anchor.
	if d.candidate.ID != "" && d.candidate.ID != d.old.RuntimeID {
		job.Containers = append(job.Containers, d.candidate.ID)
	}
	// Require the bounded cleanup precondition before changing owned state.
	if d.anchor != "" && d.anchor != d.old.Anchor {
		job.Containers = append(job.Containers, d.anchor)
	}
	return job
}

// journalFailedGroup writes retry intent before rollback consumes its bounded cleanup deadline.
func (d *dockerDeployment) journalFailedGroup() {
	job := d.failedGroup()
	// Replays of committed groups have no disposable candidate identities.
	if len(job.Containers) == 0 && job.Network == "" {
		return
	}
	// Leave remaining cleanup work retryable if this operation fails.
	if err := d.runtime.writeCleanup(job); err != nil {
		d.event("cleanup", "Unable to retain failed workload cleanup intent")
	}
}

// writeCleanup atomically retains exact retry targets across controller restarts.
func (r *Runtime) writeCleanup(job failedGroupCleanup) error {
	// Cleanup journals must never fall back to a relative working-directory path.
	if r.options.StateDir == "" {
		return fmt.Errorf("runtime cleanup requires a state directory")
	}
	raw, err := json.Marshal(job)
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil {
		return err
	}
	path := filepath.Join(r.options.StateDir, "runtime-cleanup", fmt.Sprintf("%x.json", sha256.Sum256(raw)))
	return atomicWrite(path, raw)
}

// cleanupFailedGroup uses its own deadline even when primary rollback exhausted its budget.
func (d *dockerDeployment) cleanupFailedGroup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The durable retry record remains until periodic reconciliation confirms absence.
	if err := d.runtime.cleanGroup(ctx, d.failedGroup(), protectedContainerIDs(d.s)); err != nil {
		d.event("cleanup", "Failed workload cleanup pending retry")
	}
}

// cleanGroup releases only unreferenced owned objects, leaving volumes and external containers alone.
func (r *Runtime) cleanGroup(ctx context.Context, job failedGroupCleanup, protected map[string]bool) error {
	// Inspect each owned candidate without widening cleanup authority.
	for _, id := range job.Containers {
		// A later successful replay may have adopted a previously failed candidate.
		if protected[id] {
			continue
		}
		// Stop at the first failure so an anchor never disappears before its members.
		if err := r.groupContainerAction(ctx, job.App, id, "delete", 1); err != nil {
			return err
		}
	}
	return r.cleanGroupNetwork(ctx, job, protected)
}

// protectedContainerIDs covers active, pending-drain and explicitly retained runtime groups.
func protectedContainerIDs(s state) map[string]bool {
	keep := map[string]bool{}
	// Inspect each owned candidate without widening cleanup authority.
	for _, apps := range []map[string]appRecord{s.Apps, s.Retained} {
		// Inspect each owned candidate without widening cleanup authority.
		for name, app := range apps {
			keep["network:"+name] = true
			keep[app.RuntimeID], keep[app.Anchor], keep[app.PreviousRuntimeID] = true, true, true
			// Inspect each owned candidate without widening cleanup authority.
			for _, id := range app.Members {
				keep[id] = true
			}
			// Require the bounded cleanup precondition before changing owned state.
			if app.PreviousGroup != nil {
				keep[app.PreviousGroup.Anchor] = true
				// Inspect each owned candidate without widening cleanup authority.
				for _, id := range app.PreviousGroup.Members {
					keep[id] = true
				}
			}
		}
	}
	return keep
}

// CleanupFailed retries journaled Docker deletions without adopting untracked host resources.
func (r *Runtime) CleanupFailed(ctx context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.read()
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil {
		return 0, err
	}
	dir := filepath.Join(r.options.StateDir, "runtime-cleanup")
	entries, err := os.ReadDir(dir)
	// Leave remaining cleanup work retryable if this operation fails.
	if os.IsNotExist(err) {
		return 0, nil
	}
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil {
		return 0, err
	}
	completed := 0
	var pending error
	// Inspect each owned candidate without widening cleanup authority.
	for _, entry := range entries {
		// Require the bounded cleanup precondition before changing owned state.
		if !strings.HasSuffix(entry.Name(), ".json") || !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		// Leave remaining cleanup work retryable if this operation fails.
		if err = r.retryCleanup(ctx, path, protectedContainerIDs(s)); err != nil {
			pending = err
			continue
		}
		completed++
	}
	return completed, pending
}

// retryCleanup validates a bounded journal before touching any daemon object.
func (r *Runtime) retryCleanup(ctx context.Context, path string, protected map[string]bool) error {
	info, err := os.Lstat(path)
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return fmt.Errorf("invalid runtime cleanup journal")
	}
	raw, err := os.ReadFile(path)
	var job failedGroupCleanup
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil || json.Unmarshal(raw, &job) != nil || job.App == "" {
		return fmt.Errorf("invalid runtime cleanup intent")
	}
	// Leave remaining cleanup work retryable if this operation fails.
	if err = r.cleanGroup(ctx, job, protected); err != nil {
		return err
	}
	return os.Remove(path)
}

// RemoveArtifact evicts only an exact Hostlier image tag and never forces deletion of in-use images.
func (r *Runtime) RemoveArtifact(ctx context.Context, image string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Require the bounded cleanup precondition before changing owned state.
	if !artifact.IsImage(image) || !digestPattern.MatchString(image) {
		return model.Err("ARTIFACT_ID", "Invalid cleanup image")
	}
	err := r.api(ctx, "DELETE", "/images/"+url.PathEscape(artifact.RuntimeImage(image))+"?force=false&noprune=false", nil, nil, nil)
	// Leave remaining cleanup work retryable if this operation fails.
	if hasCode(err, "HTTP_404") {
		return nil
	}
	return err
}
