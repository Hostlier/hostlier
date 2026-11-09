package controller

import (
	"context"
	"errors"
	"fmt"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"path/filepath"
	"time"
)

// artifactCleaner exposes only exact Hostlier image eviction, never a host-wide prune.
type artifactCleaner interface {
	RemoveArtifact(context.Context, string) error
}

// failedWorkloadCleaner retries durable runtime-owned cleanup intents.
type failedWorkloadCleaner interface {
	CleanupFailed(context.Context) (int, error)
}

// startCleanupMonitor owns startup and periodic cleanup under the controller lifecycle.
func (c *Controller) startCleanupMonitor() {
	cleaner, _ := c.opts.Runtime.(artifactCleaner)
	// Finish filesystem-only startup cleanup before exposing an external-runtime controller.
	if cleaner == nil {
		c.cleanupOnce(nil)
	}
	c.wg.Add(1)
	go func() { // This worker exits with the controller and never outlives its state store.
		defer c.wg.Done()
		// Daemon cleanup remains asynchronous so an unavailable daemon cannot delay startup.
		if cleaner != nil {
			c.cleanupOnce(cleaner)
		}
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		// Inspect each owned candidate without widening cleanup authority.
		for {
			// Require the bounded cleanup precondition before changing owned state.
			select {
			case <-c.ctx.Done(): // Shutdown cancels pending daemon cleanup.
				return
			case <-ticker.C: // Retry failures and expire idle content once per minute.
				c.cleanupOnce(cleaner)
			}
		}
	}()
}

// cleanupOnce serializes reclamation with deployments while allowing ordinary status reads.
func (c *Controller) cleanupOnce(cleaner artifactCleaner) {
	c.execMu.Lock()
	defer c.execMu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	store := artifact.Store{Root: filepath.Join(c.opts.StateDir, "artifacts")}
	c.mu.Lock()
	keep := c.protectedArtifacts()
	result, err := store.Sweep(c.opts.Now(), keep)
	uploads, uploadErr := c.packageUploads().Sweep(c.opts.Now())
	result.Files += uploads.Files
	result.Bytes += uploads.Bytes
	c.mu.Unlock()
	// Invalid inventory prevents image reclamation but does not block independent container cleanup.
	if err == nil && cleaner != nil {
		err = cleanupImages(ctx, store, cleaner, keep)
	}
	err = errors.Join(err, uploadErr)
	groups := 0
	// Require the bounded cleanup precondition before changing owned state.
	if runtime, ok := c.opts.Runtime.(failedWorkloadCleaner); ok {
		var cleanupErr error
		groups, cleanupErr = runtime.CleanupFailed(ctx)
		err = errors.Join(err, cleanupErr)
	}
	c.recordCleanup(result, groups, err)
}

// cleanupImages keeps eviction retries durable and preserves newly protected image references.
func cleanupImages(ctx context.Context, store artifact.Store, cleaner artifactCleaner, keep map[string]bool) error {
	images, err := store.PendingImages()
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil {
		return err
	}
	var pending error
	// Inspect each owned candidate without widening cleanup authority.
	for _, image := range images {
		// A reused image supersedes the old eviction intent without touching its runtime cache.
		if !keep[image] {
			// Leave remaining cleanup work retryable if this operation fails.
			if err = cleaner.RemoveArtifact(ctx, image); err != nil {
				pending = err
				continue
			}
		}
		// Leave remaining cleanup work retryable if this operation fails.
		if err = store.ForgetImage(image); err != nil {
			return err
		}
	}
	return pending
}

// protectedArtifacts includes every deployable current, rollback, plan and unfinished operation input.
func (c *Controller) protectedArtifacts() map[string]bool {
	keep := map[string]bool{}
	// Inspect each owned candidate without widening cleanup authority.
	for _, app := range c.state.Applications {
		// Deleted apps retain data metadata, but no runnable workload or image lease.
		if app.Status == "deleted" {
			continue
		}
		retainProjectArtifacts(keep, app.Spec)
		// Inspect each owned candidate without widening cleanup authority.
		for _, revision := range app.Revisions {
			retainProjectArtifacts(keep, revision.Spec)
		}
	}
	// Inspect each owned candidate without widening cleanup authority.
	for _, plan := range c.state.Plans {
		// Require the bounded cleanup precondition before changing owned state.
		if plan.ExpiresAt > c.opts.Now().UnixMilli() {
			retainProjectArtifacts(keep, plan.Spec)
		}
	}
	// Inspect each owned candidate without widening cleanup authority.
	for _, operation := range c.state.Operations {
		// Recovery work retains its inputs until explicitly completed or superseded.
		if operation.Status != "succeeded" && operation.Status != "failed" && operation.SupersededBy == "" {
			retainProjectArtifacts(keep, operation.Spec)
		}
	}
	return keep
}

// retainProjectArtifacts follows nested sidecars without depending on source normalization.
func retainProjectArtifacts(keep map[string]bool, project model.Project) {
	keep[project.Image] = true
	// Require the bounded cleanup precondition before changing owned state.
	if project.Source != nil {
		keep[project.Source.Image] = true
	}
	// Inspect each owned candidate without widening cleanup authority.
	for _, sidecar := range project.Sidecars {
		retainProjectArtifacts(keep, sidecar)
	}
}

// recordCleanup reports counts and retry state without persisting daemon diagnostics or secret-bearing logs.
func (c *Controller) recordCleanup(result artifact.CleanupResult, groups int, err error) {
	// Leave remaining cleanup work retryable if this operation fails.
	if result.Files == 0 && groups == 0 && err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	message := fmt.Sprintf("Automatic cleanup: %d files, %d bytes, %d failed workload groups", result.Files, result.Bytes, groups)
	level := "info"
	// Leave remaining cleanup work retryable if this operation fails.
	if err != nil {
		message += "; pending cleanup will retry"
		level = "warn"
	}
	c.log(message, "", level)
	_ = c.store.Save(c.state)
}
