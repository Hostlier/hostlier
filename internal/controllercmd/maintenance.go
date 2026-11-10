package controllercmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hostlier/internal/migration"
	"hostlier/internal/model"
)

// PreviewMigration inspects committed state and host facts without stopping any service.
func (m *serverMaintenance) PreviewMigration(ctx context.Context, backup string) (json.RawMessage, error) {
	p, err := migration.Inspect(ctx, migration.Options{StateDir: m.stateDir, Config: m.configPath, BackupDir: backup})
	// Inspection errors may include root-only diagnostic output.
	if err != nil {
		return nil, model.Err("MIGRATION_PREVIEW_FAILED", "Cannot inspect migration; use a new absolute backup directory outside controller state and inspect controller diagnostics")
	}
	raw, err := json.Marshal(p)
	return raw, err
}

// PreviewUpdate retains verified release bytes and a concrete installation review.
func (m *serverMaintenance) PreviewUpdate(ctx context.Context, actor string, generation int64) (model.ControllerUpdatePlan, error) {
	// Release staging never holds a mutex needed by the controller dispatcher during network I/O.
	if err := m.checkHost(m.stateDir); err != nil {
		return model.ControllerUpdatePlan{}, maintenanceUnavailable(err)
	}
	record, dir, err := m.allocateUpdateStage(actor, generation)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return model.ControllerUpdatePlan{}, err
	}
	// Failed preparations never leave an executable job behind.
	if err := m.prepare(ctx, &record, dir); err != nil {
		_ = os.RemoveAll(dir)
		return model.ControllerUpdatePlan{}, maintenanceUnavailable(err)
	}
	// Keep the initiating executable available independently of installed-binary replacement.
	if err := m.retainRunner(&record, dir); err != nil {
		_ = os.RemoveAll(dir)
		return model.ControllerUpdatePlan{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record.Plan.ExpiresAt = m.now().Add(updateReviewLifetime).UnixMilli()
	record.Plan.Fingerprint = updateFingerprint(record.Plan)
	// Flush the private receipt before crossing the next execution or response boundary.
	if err := saveControllerUpdate(dir, record); err != nil {
		return model.ControllerUpdatePlan{}, err
	}
	return record.Plan, nil
}

// allocateUpdateStage reserves bounded disk staging while keeping network work outside the shared lock.
func (m *serverMaintenance) allocateUpdateStage(actor string, generation int64) (controllerUpdateRecord, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Reclaim expired unconsumed reviews before allocating another release stage.
	if err := m.pruneUpdateReviews(); err != nil {
		return controllerUpdateRecord{}, "", err
	}
	id, err := newMaintenanceID()
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return controllerUpdateRecord{}, "", err
	}
	dir, _ := m.stagePath(id)
	// Create the protected destination before retaining executable update material.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return controllerUpdateRecord{}, "", err
	}
	record := controllerUpdateRecord{Plan: model.ControllerUpdatePlan{ID: id, ExpiresAt: m.now().Add(20 * time.Minute).UnixMilli()}, Actor: actor, Generation: generation, ConfigPath: m.configPath}
	// Flush the private receipt before crossing the next execution or response boundary.
	if err := saveControllerUpdate(dir, record); err != nil {
		return record, dir, err
	}
	return record, dir, nil
}

// retainRunner binds the old executable used by the independent restart job.
func (m *serverMaintenance) retainRunner(record *controllerUpdateRecord, dir string) error {
	// A complete private runner copy is required before reporting a usable review.
	if err := copyMaintenanceRunner(m.executable, dir); err != nil {
		return err
	}
	sum, err := maintenanceHash(filepath.Join(dir, "runner"))
	record.SourceSHA256 = sum
	return err
}

// ReviewUpdate rejects substituted, foreign, expired or stale reviews before approval.
func (m *serverMaintenance) ReviewUpdate(ctx context.Context, actor string, generation int64, input model.ControllerUpdateRequest) (model.ControllerUpdatePlan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, _, err := m.reviewRecord(actor, generation, input)
	return record.Plan, err
}

// reviewRecord is shared by approval review and the final scheduling boundary.
func (m *serverMaintenance) reviewRecord(actor string, generation int64, input model.ControllerUpdateRequest) (controllerUpdateRecord, string, error) {
	dir, err := m.stagePath(input.ID)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return controllerUpdateRecord{}, "", err
	}
	record, err := readControllerUpdate(dir)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return record, dir, model.Err("NOT_FOUND", "Controller update review not found")
	}
	// Reject foreign or changed reviews before considering their execution state.
	if err := m.validateReview(record, actor, generation, input); err != nil {
		return record, dir, err
	}
	return record, dir, nil
}

// validateReview binds the public review to its original login and request before use.
func (m *serverMaintenance) validateReview(record controllerUpdateRecord, actor string, generation int64, input model.ControllerUpdateRequest) error {
	// A request identity is required even when the approval layer also deduplicates.
	if record.Actor != actor || !maintenanceID.MatchString(input.RequestID) {
		return model.Err("SCOPE_DENIED", "Controller update review belongs to another request or login")
	}
	// Bind execution to every public review field and its retained fingerprint.
	if record.Plan.Fingerprint != input.Fingerprint || updateFingerprint(record.Plan) != input.Fingerprint {
		return model.Err("STALE_PLAN", "Controller update review fingerprint changed")
	}
	return m.validateReviewState(record, generation, input.RequestID)
}

// validateReviewState distinguishes a durable receipt replay from a new stale execution.
func (m *serverMaintenance) validateReviewState(record controllerUpdateRecord, generation int64, requestID string) error {
	// A consumed review is replayable only with its original durable request identity.
	if record.RequestID != "" {
		// A consumed review belongs exclusively to its original durable request identity.
		if record.RequestID != requestID {
			return model.Err("REQUEST_CONFLICT", "Controller update review already consumed")
		}
		return nil
	}
	// Changed controller state or expired authority requires a new inspection.
	if record.Generation != generation || record.Plan.ExpiresAt <= m.now().UnixMilli() {
		return model.Err("STALE_PLAN", "Controller state or update review changed; preview again")
	}
	return nil
}

// StartUpdate records acceptance before launching a detached, fixed-purpose unit.
func (m *serverMaintenance) StartUpdate(ctx context.Context, actor string, generation int64, input model.ControllerUpdateRequest) (model.ControllerUpdateJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, dir, err := m.reviewRecord(actor, generation, input)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return model.ControllerUpdateJob{}, err
	}
	// A consumed review returns its receipt rather than scheduling another unit.
	if record.RequestID != "" {
		return record.Job, nil
	}
	// Block unsupported target installations before reserving execution.
	if len(record.Plan.Errors) > 0 {
		return model.ControllerUpdateJob{}, model.Err("INSTALL_PREFLIGHT", "Resolve the controller update blockers first")
	}
	// Staged root code requires protected host-owned state and ancestors.
	if err := m.checkHost(dir); err != nil {
		return model.ControllerUpdateJob{}, maintenanceUnavailable(err)
	}
	// Recheck both retained executable hashes immediately before privileged execution.
	if err := verifyUpdatePayload(dir, record); err != nil {
		return model.ControllerUpdateJob{}, err
	}
	record.RequestID = input.RequestID
	record.QueuedAt = m.now().UnixMilli()
	record.Job = model.ControllerUpdateJob{ID: record.Plan.ID, Version: record.Plan.Version, Status: "queued", Step: "waiting-for-independent-runner"}
	// Flush the private receipt before crossing the next execution or response boundary.
	if err := saveControllerUpdate(dir, record); err != nil {
		return model.ControllerUpdateJob{}, err
	}
	// Scheduling failures are durable terminal outcomes and never implicitly replayed.
	if err := m.schedule(ctx, m.stateDir, dir, input.ID); err != nil {
		record.Job.Status = "failed"
		record.Job.Error = &model.Error{Code: "UPDATE_SCHEDULE_FAILED", Message: "Cannot schedule controller update; inspect retained server diagnostics"}
		// Flush the private receipt before crossing the next execution or response boundary.
		if err := saveControllerUpdate(dir, record); err != nil {
			return model.ControllerUpdateJob{}, err
		}
	}
	return record.Job, nil
}

// UpdateStatus reads a receipt independently of the daemon process that accepted it.
func (m *serverMaintenance) UpdateStatus(ctx context.Context, actor, id string) (model.ControllerUpdateJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, err := m.stagePath(id)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return model.ControllerUpdateJob{}, err
	}
	record, err := readControllerUpdate(dir)
	// Only accepted jobs have public restart receipts.
	if err != nil || record.RequestID == "" {
		return model.ControllerUpdateJob{}, model.Err("NOT_FOUND", "Controller update job not found")
	}
	// A remote login cannot inspect another login's retained update job.
	if actor != "local" && actor != record.Actor {
		return model.ControllerUpdateJob{}, model.Err("SCOPE_DENIED", "Controller update job belongs to another login")
	}
	// A missing transient unit after reboot is an interrupted job, never a retry.
	if m.updateRunnerLost(ctx, record) {
		record.Job.Status = "interrupted"
		record.Job.Error = &model.Error{Code: "UPDATE_INTERRUPTED", Message: "Controller update runner stopped; inspect installation before creating a new review"}
		// Flush the private receipt before crossing the next execution or response boundary.
		if err := saveControllerUpdate(dir, record); err != nil {
			return model.ControllerUpdateJob{}, err
		}
	}
	return record.Job, nil
}

// updateRunnerLost requires positive evidence that no timer or service owns the job.
// A systemd timeout or inspection failure keeps the reservation intact.
func (m *serverMaintenance) updateRunnerLost(ctx context.Context, record controllerUpdateRecord) bool {
	// Fresh acceptance still permits systemd to instantiate its service and timer.
	if !updatePending(record.Job.Status) || m.now().UnixMilli()-record.QueuedAt <= 60000 {
		return false
	}
	active, err := m.active(ctx, record.Plan.ID)
	return err == nil && !active
}

// updatePending enumerates states that reserve controller maintenance authority.
func updatePending(status string) bool { return status == "queued" || status == "running" }

// verifyUpdatePayload prevents staged executable substitution between review and execution.
func verifyUpdatePayload(dir string, record controllerUpdateRecord) error {
	// Both independently executed programs must still match the retained hashes.
	for name, want := range map[string]string{"runner": record.SourceSHA256, "hostlier-controller": record.TargetSHA256} {
		got, err := maintenanceHash(filepath.Join(dir, name))
		// Reject incomplete hashes or executable substitution before starting root code.
		if err != nil || want == "" || got != want {
			return model.Err("UPDATE_STATE", "Staged controller update failed integrity verification")
		}
	}
	return nil
}

// safeMaintenanceArgument prevents systemd specifier and environment reinterpretation.
func safeMaintenanceArgument(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsAny(value, "%$\r\n\x00")
}

// pruneUpdateReviews expires only unconsumed reviews and caps simultaneous release staging.
func (m *serverMaintenance) pruneUpdateReviews() error {
	root := filepath.Join(m.stateDir, "controller-updates")
	// Create the protected destination before retaining executable update material.
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	pending := 0
	// Accepted jobs and their retained logs are never garbage-collected by a preview.
	for _, entry := range entries {
		// Ignore names outside the server-generated update identity format.
		if !maintenanceID.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		record, err := readControllerUpdate(dir)
		// A consumed review returns its receipt rather than scheduling another unit.
		if err != nil || record.RequestID != "" {
			continue
		}
		// Expired unconsumed reviews no longer authorize execution or retain their payloads.
		if record.Plan.ExpiresAt <= m.now().UnixMilli() {
			// Remove only expired, unconsumed stage data after validating its record.
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			continue
		}
		pending++
	}
	// Bound simultaneous release staging without deleting accepted-job diagnostics.
	if pending >= 3 {
		return model.Err("UPDATE_REVIEW_LIMIT", "Three controller update reviews are already staged; wait for their ten-minute expiry")
	}
	return nil
}
