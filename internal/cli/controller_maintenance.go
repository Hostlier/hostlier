package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// controllerMaintenanceCommand validates the fixed remote maintenance surface before connecting further.
func (s *session) controllerMaintenanceCommand() error {
	// Dispatch only the fixed maintenance operations supported by this interface.
	switch s.sub {
	// Review and apply one verified official release through retained approval.
	case "update":
		// Reject ambiguous positionals before selecting a privileged server operation.
		if len(s.parsed.args) != 2 {
			return model.Err("ARGUMENTS", "Usage: hostlier server controller update [--dry-run] [--detach] [--server NAME]")
		}
		return s.updateController()
	// Inspect or resume one durable receipt after an API restart.
	case "update-status":
		// Reject ambiguous positionals before selecting a privileged server operation.
		if len(s.parsed.args) != 3 || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(s.target) {
			return model.Err("ARGUMENTS", "Usage: hostlier server controller update-status ID [--watch] [--server NAME]")
		}
		return s.controllerUpdateStatus(s.target, s.flags["watch"] != "")
	// Remote migration execution is deliberately absent from this read-only command.
	case "migrate":
		// Preparation is another fixed read-only route; local remediation is never exposed.
		if s.validPreparationPreviewInvocation() {
			return s.previewControllerPreparation()
		}
		// Require the read-only command and an explicit server backup destination.
		if !s.validMigrationPreviewInvocation() {
			return model.Err("ARGUMENTS", "Usage: hostlier server controller migrate plan --to k3s --backup-dir NEW_SERVER_DIRECTORY [--server NAME]")
		}
		return s.previewControllerMigration()
	}
	return model.Err("ARGUMENTS", "Unsupported controller maintenance command")
}

// validPreparationPreviewInvocation admits only the fixed read-only prerequisite preview.
func (s *session) validPreparationPreviewInvocation() bool {
	return len(s.parsed.args) == 3 && s.target == "prepare" && s.flags["backup-dir"] != ""
}

// previewControllerPreparation inspects reversible prerequisite actions without SSH or host changes.
func (s *session) previewControllerPreparation() error {
	// New prerequisite previews require an explicitly advertised server capability.
	if err := s.requireControllerCapability("migrationPreparation"); err != nil {
		return err
	}
	query := url.Values{"backupDir": {s.flags["backup-dir"]}}
	return s.getPrint("/v1/controller/migration/preparation?" + query.Encode())
}

// validMigrationPreviewInvocation admits only the read-only Docker to k3s preview.
func (s *session) validMigrationPreviewInvocation() bool {
	return len(s.parsed.args) == 3 && s.target == "plan" && defaultValue(s.flags["to"], "k3s") == "k3s" && s.flags["backup-dir"] != ""
}

// requireControllerCapability explains old-controller bootstrap without attempting SSH fallback.
func (s *session) requireControllerCapability(feature string) error {
	var capabilities map[string]bool
	err := s.request("GET", "/v1/controller/capabilities", nil, &capabilities)
	// Authentication and network failures retain their existing, actionable meaning.
	if err != nil && planningErrorCode(err) != "NOT_FOUND" {
		return err
	}
	// Explain the initial server upgrade instead of attempting another transport.
	if !capabilities[feature] {
		// A maintenance-capable controller can perform its own approved upgrade without SSH.
		if capabilities["updates"] {
			return model.Err("CONTROLLER_UPGRADE_REQUIRED", "Upgrade this controller through server controller update to enable the requested maintenance feature")
		}
		return model.Err("CONTROLLER_UPGRADE_REQUIRED", "This controller needs one initial administrator upgrade to a maintenance-capable release; updating the CLI alone cannot add server support")
	}
	return nil
}

// previewControllerMigration reads the configured server using the saved login.
func (s *session) previewControllerMigration() error {
	// Confirm server-side support before requesting maintenance work.
	if err := s.requireControllerCapability("migrationPreview"); err != nil {
		return err
	}
	query := url.Values{"to": {"k3s"}, "backupDir": {s.flags["backup-dir"]}}
	return s.getPrint("/v1/controller/migration/plan?" + query.Encode())
}

// updateController presents the server-retained review before requesting independent approval.
func (s *session) updateController() error {
	// Keep broad command-line consent outside the independent approval protocol.
	if s.flags["yes"] != "" {
		return s.approve("")
	}
	// Confirm server-side support before requesting maintenance work.
	if err := s.requireControllerCapability("updates"); err != nil {
		return err
	}
	var plan model.ControllerUpdatePlan
	// Keep transport or server rejection distinct from accepted maintenance work.
	if err := s.request("GET", "/v1/controller/update/plan", nil, &plan); err != nil {
		return err
	}
	// Return the concrete review without submitting any installation request.
	if s.flags["dry-run"] != "" {
		return s.print(plan)
	}
	// Display the complete review before requesting authority to execute it.
	if err := s.printReview(plan); err != nil {
		return err
	}
	// An affirmative approval cannot override installation preflight blockers.
	if len(plan.Errors) > 0 {
		return model.Err("INSTALL_PREFLIGHT", "Resolve the reported controller update blockers before applying")
	}
	return s.applyControllerUpdate(plan)
}

// applyControllerUpdate submits only the exact review and observes the returned durable job.
func (s *session) applyControllerUpdate(plan model.ControllerUpdatePlan) error {
	// Require consent through the existing exact-request approval machinery.
	if err := s.approve("Apply this controller update and briefly restart its API?"); err != nil {
		return err
	}
	input := model.ControllerUpdateRequest{ID: plan.ID, Fingerprint: plan.Fingerprint, RequestID: client.RequestID()}
	var job model.ControllerUpdateJob
	// Keep transport or server rejection distinct from accepted maintenance work.
	if err := s.request("POST", "/v1/controller/update/apply", input, &job); err != nil {
		return err
	}
	// Retain a visible job locator before following the controller restart.
	if _, err := fmt.Fprintf(s.errOut, "Controller update job: %s\nResume: hostlier server controller update-status %s --watch --server %s\n", job.ID, job.ID, defaultValue(s.masterProfile, "default")); err != nil {
		return err
	}
	// Return durable acceptance when the caller chose detached observation.
	if s.flags["detach"] != "" {
		return s.print(job)
	}
	return s.controllerUpdateStatus(job.ID, true)
}

// controllerUpdateStatus tolerates API downtime while retaining the exact accepted job identity.
func (s *session) controllerUpdateStatus(id string, watch bool) error {
	// Cancellation never cancels or replays the independently accepted server job.
	original := s.ctx
	ctx, cancel := context.WithTimeout(s.ctx, 35*time.Minute)
	defer cancel()
	s.ctx = ctx
	// Restore the invocation context after this bounded status observation.
	defer func() { s.ctx = original }()
	// Inspect every selected item before advancing the maintenance boundary.
	for {
		var job model.ControllerUpdateJob
		err := s.request("GET", "/v1/controller/update/jobs/"+id, nil, &job)
		// Only transport failures are expected during the API restart.
		if err != nil && !controllerRestartConnection(err) {
			return err
		}
		// An inspection returns immediately without polling or resubmitting work.
		if err == nil && (!watch || job.Status != "queued" && job.Status != "running") {
			return s.printControllerUpdateResult(job)
		}
		// An inspection returns immediately without polling or resubmitting work.
		if !watch {
			return err
		}
		// Cancellation ends local observation while retaining the accepted server job.
		if err := s.pause(); err != nil {
			return model.Err("UPDATE_WAIT_ENDED", "Stopped waiting; resume the retained update with controller update-status "+id+" --watch")
		}
	}
}

// printControllerUpdateResult preserves the receipt and a failing process exit status.
func (s *session) printControllerUpdateResult(job model.ControllerUpdateJob) error {
	// Publish the receipt before returning its failing process status.
	if err := s.print(job); err != nil {
		return err
	}
	// Preserve the server-retained failure code for scripts and operators.
	if job.Error != nil {
		return job.Error
	}
	// An unsuccessful terminal receipt must produce a failing CLI exit status.
	if job.Status == "failed" || job.Status == "interrupted" {
		return model.Err("UPDATE_FAILED", "Controller update did not complete; inspect its retained receipt")
	}
	return nil
}

// controllerRestartConnection recognizes temporary transport failures during a daemon restart.
func controllerRestartConnection(err error) bool {
	var typed *model.Error
	return errors.As(err, &typed) && (typed.Code == "CONNECTION" || typed.Code == "UNAVAILABLE")
}
