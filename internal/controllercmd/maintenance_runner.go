package controllercmd

import (
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"hostlier/internal/controller"
	"hostlier/internal/model"
	"hostlier/internal/store"
)

// updateRunnerDependencies permits service-boundary tests without changing a real host.
type updateRunnerDependencies struct {
	service  func(context.Context, string) error
	install  func(context.Context, string, controllerUpdateRecord, io.Writer) error
	snapshot func(string, *controller.State) error
}

// runControllerUpdate is a root-only entry point with no arbitrary process or path parameters.
func runControllerUpdate(ctx context.Context, args []string, errOut io.Writer) error {
	flags := flag.NewFlagSet("controller maintenance runner", flag.ContinueOnError)
	flags.SetOutput(errOut)
	stateDir := flags.String("state-dir", "/var/lib/hostlier", "installed state")
	id := flags.String("id", "", "retained update identity")
	// Reject runner arguments before reading or changing privileged state.
	if err := flags.Parse(args); err != nil {
		return err
	}
	// The runner accepts fixed flags, never caller-selected executable commands.
	if flags.NArg() != 0 {
		return model.Err("ARGUMENTS", "Maintenance runner accepts flags only")
	}
	// Only supported root-owned Linux installations can execute maintenance.
	if err := requireUpdateHost(*stateDir); err != nil {
		return err
	}
	maintenance := newServerMaintenance(*stateDir, "")
	dir, err := maintenance.stagePath(*id)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	// Reject writable or symlinked stage ancestors before opening root execution records.
	if err := protectedUpdatePath(dir); err != nil {
		return err
	}
	// The independent runner has a finite lifetime even if its caller has none.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	// Read committed state without opening another controller writer.
	deps := updateRunnerDependencies{service: updateControllerService, install: installControllerUpdate, snapshot: func(path string, s *controller.State) error { return store.Snapshot(path, s) }}
	return executeControllerUpdate(ctx, *stateDir, dir, deps)
}

// executeControllerUpdate consumes one queued job and always attempts API recovery on failure.
func executeControllerUpdate(ctx context.Context, stateDir, dir string, deps updateRunnerDependencies) error {
	record, err := readControllerUpdate(dir)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	// Only accepted jobs have public restart receipts.
	if record.Job.Status != "queued" || record.RequestID == "" {
		return model.Err("UPDATE_STATE", "Controller update job is not queued; automatic replay is forbidden")
	}
	// Recheck both retained executable hashes immediately before privileged execution.
	if err := verifyUpdatePayload(dir, record); err != nil {
		return finishControllerUpdate(dir, record, err)
	}
	record.Job.Status = "running"
	record.Job.Step = "stopping-controller"
	// Flush the private receipt before crossing the next execution or response boundary.
	if err := saveControllerUpdate(dir, record); err != nil {
		return err
	}
	err = performControllerUpdate(ctx, stateDir, dir, &record, deps)
	// Cleanup is independent of an expired installer context.
	recoveryCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Controller shutdown failure must prevent installation; restart uses an independent deadline.
	if restartErr := deps.service(recoveryCtx, "start"); restartErr != nil {
		err = model.Err("UPDATE_RECOVERY_FAILED", "Controller service could not be restarted; server administration is required")
	}
	return finishControllerUpdate(dir, record, err)
}

// performControllerUpdate verifies the committed reservation after gracefully stopping the daemon.
func performControllerUpdate(ctx context.Context, stateDir, dir string, record *controllerUpdateRecord, deps updateRunnerDependencies) error {
	// Controller shutdown failure must prevent installation; restart uses an independent deadline.
	if err := deps.service(ctx, "stop"); err != nil {
		return model.Err("UPDATE_STOP_FAILED", "Cannot stop the controller safely")
	}
	var state controller.State
	// Verify the final committed source after the controller has stopped writing.
	if err := deps.snapshot(stateDir, &state); err != nil {
		return model.Err("UPDATE_STATE", "Cannot verify committed controller state")
	}
	// Changed controller state or expired authority requires a new inspection.
	if state.ControllerUpdateID != record.Plan.ID || state.Generation != record.Generation {
		return model.Err("STALE_PLAN", "Controller state changed before the update started; preview again")
	}
	record.Job.Step = "installing-verified-release"
	// Flush the private receipt before crossing the next execution or response boundary.
	if err := saveControllerUpdate(dir, *record); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "installer.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	defer log.Close()
	return deps.install(ctx, dir, *record, log)
}

// finishControllerUpdate retains a truthful terminal receipt instead of replaying uncertain work.
func finishControllerUpdate(dir string, record controllerUpdateRecord, err error) error {
	record.Job.Status = "succeeded"
	record.Job.Step = "completed"
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		record.Job.Status = "failed"
		record.Job.Step = "inspect-retained-installation"
		record.Job.Error = &model.Error{Code: "UPDATE_FAILED", Message: "Controller update failed; inspect the retained installer log before retrying"}
		// Known service and stale-plan failures contain no subprocess output.
		if typed, ok := err.(*model.Error); ok {
			record.Job.Error = typed
		}
	}
	// Flush the private receipt before crossing the next execution or response boundary.
	if saveErr := saveControllerUpdate(dir, record); saveErr != nil {
		return saveErr
	}
	return err
}

// updateControllerService never stops Docker, application containers or the public proxy.
func updateControllerService(ctx context.Context, action string) error {
	return exec.CommandContext(ctx, "systemctl", action, "hostlier.service").Run()
}

// installControllerUpdate delegates to the verified release's fingerprint-bound installer.
func installControllerUpdate(ctx context.Context, dir string, record controllerUpdateRecord, log io.Writer) error {
	args := append(append([]string{}, record.InitArgs...), "--approved-fingerprint", record.InstallerFingerprint, "--yes", "--json")
	command := exec.CommandContext(ctx, filepath.Join(dir, "hostlier-controller"), args...)
	command.Stdout, command.Stderr = log, log
	command.Env = withoutGitHubTokens(os.Environ())
	return command.Run()
}
