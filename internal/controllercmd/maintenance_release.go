package controllercmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"hostlier/internal/installer"
	"hostlier/internal/model"
)

const updateReviewLifetime = 10 * time.Minute

// prepareControllerRelease downloads only the verified official stable server bundle.
func prepareControllerRelease(ctx context.Context, record *controllerUpdateRecord, dir string) error {
	stateDir := filepath.Dir(filepath.Dir(dir))
	installed, domain, err := installedUpdateTarget(stateDir, record.ConfigPath)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	client := updateClient()
	// Verify the fixed repository identity before trusting its release metadata.
	if err := verifyOfficialRepository(ctx, client); err != nil {
		return err
	}
	release, err := latestControllerRelease(ctx, client, runtime.GOARCH)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	archive, err := downloadControllerBundle(ctx, client, release, runtime.GOARCH, dir)
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return err
	}
	// Only the checked release layout may provide the staged installer and collector.
	if err := extractControllerBundle(archive, dir); err != nil {
		return err
	}
	// Expanded payloads are retained; the already-verified archive need not occupy disk twice.
	if err := os.Remove(archive); err != nil {
		return err
	}
	record.InitArgs = updateInitArgs(installed, domain, stateDir, record.ConfigPath)
	return inspectControllerRelease(ctx, record, dir, release)
}

// updateInitArgs fixes every installer target to the existing server installation.
func updateInitArgs(installed installer.Installation, domain, stateDir, configPath string) []string {
	args := []string{"init", "--domain", domain, "--controller-hostname", installed.ControllerHostname, "--runtime", installed.Runtime, "--proxy", installed.Proxy, "--state-dir", stateDir, "--config", configPath}
	// Retain the installed fleet trust pin instead of selecting a new controller.
	if installed.MasterFingerprint != "" {
		args = append(args, "--master-fingerprint", installed.MasterFingerprint)
	}
	return args
}

// inspectControllerRelease asks the target installer for the same review later used by apply.
func inspectControllerRelease(ctx context.Context, record *controllerUpdateRecord, dir string, release githubRelease) error {
	target := filepath.Join(dir, "hostlier-controller")
	help := exec.CommandContext(ctx, target, "init", "--help")
	help.Env = withoutGitHubTokens(os.Environ())
	output, err := help.CombinedOutput()
	// Older releases cannot enforce the retained approval fingerprint at execution.
	if err != nil || !bytes.Contains(output, []byte("-approved-fingerprint")) {
		return model.Err("RELEASE_UNSUPPORTED", "Latest official release does not support reviewed remote updates yet")
	}
	command := exec.CommandContext(ctx, target, append(record.InitArgs, "--dry-run", "--json")...)
	command.Env = withoutGitHubTokens(os.Environ())
	output, err = command.Output()
	// Preserve the prerequisite failure before advancing privileged maintenance or reporting success.
	if err != nil {
		return model.Err("INSTALL_PREFLIGHT", "The verified release could not inspect this installation")
	}
	var plan installer.Plan
	// Reject malformed retained or target data before treating it as executable authority.
	if err := json.Unmarshal(output, &plan); err != nil {
		return model.Err("RELEASE_UNSUPPORTED", "Release returned an invalid installation preview")
	}
	// The target release must provide an immutable inspection fingerprint.
	if plan.Fingerprint == "" {
		return model.Err("RELEASE_UNSUPPORTED", "Release did not bind its installation preview")
	}
	record.Plan = model.ControllerUpdatePlan{ID: record.Plan.ID, Version: release.TagName, Architecture: runtime.GOARCH, BundleSHA256: releaseAssetDigest(release, runtime.GOARCH), Runtime: plan.Runtime, Proxy: plan.Proxy, ControllerHostname: plan.ControllerHostname, InstallationFingerprint: plan.Fingerprint, Changes: plan.Changes, Warnings: plan.Warnings, Errors: plan.Errors}
	// A malformed unsupported plan must remain blocked even without explanatory errors.
	if !plan.Supported && len(record.Plan.Errors) == 0 {
		record.Plan.Errors = append(record.Plan.Errors, "Release reports this installation as unsupported")
	}
	record.Plan.Changes = append(record.Plan.Changes, "Restart the controller API; retain the installed runtime, proxy, application definitions and data")
	record.InstallerFingerprint = plan.Fingerprint
	record.Executable = plan.Executable
	record.TargetSHA256, err = maintenanceHash(target)
	return err
}

// scheduleControllerUpdate moves execution outside the daemon's restart cgroup.
func scheduleControllerUpdate(ctx context.Context, stateDir, dir, id string) error {
	// systemd receives fixed argv with no shell or caller-supplied command strings.
	if !safeMaintenanceArgument(stateDir) || !safeMaintenanceArgument(dir) || !maintenanceID.MatchString(id) {
		return model.Err("UPDATE_STATE", "Controller paths contain unsupported systemd substitutions")
	}
	command := exec.CommandContext(ctx, "systemd-run", "--unit=hostlier-update-"+id, "--on-active=5s", "--timer-property=AccuracySec=1s", "--collect", "--property=Type=oneshot", "--property=TimeoutStartSec=35min", filepath.Join(dir, "runner"), "maintenance-run", "--state-dir", stateDir, "--id", id)
	return command.Run()
}

// controllerUpdateActive distinguishes retained jobs from lost transient units after reboot.
func controllerUpdateActive(ctx context.Context, id string) (bool, error) {
	var inspectionErr error
	// Timer and service lifetimes do not depend on the controller daemon.
	for _, suffix := range []string{".timer", ".service"} {
		active, err := inspectUpdateUnit(ctx, "hostlier-update-"+id+suffix)
		// Either active unit is sufficient evidence that the job still owns execution.
		if active {
			return true, nil
		}
		// Inspection failures cannot authorize releasing a potentially running job.
		if err != nil {
			inspectionErr = err
		}
	}
	return false, inspectionErr
}

// inspectUpdateUnit distinguishes absent or inactive units from an unavailable system manager.
func inspectUpdateUnit(ctx context.Context, unit string) (bool, error) {
	command := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveState", "--value", unit)
	raw, err := command.Output()
	// Exit code four explicitly denotes an unknown unit, not a failed bus connection.
	if err != nil {
		var exited *exec.ExitError
		// An absent unit is a definitive inactive result; other manager failures remain uncertain.
		if errors.As(err, &exited) && exited.ExitCode() == 4 {
			return false, nil
		}
		return false, err
	}
	state := strings.TrimSpace(string(raw))
	// Known terminal unit states prove no runner remains in this unit.
	if state == "inactive" || state == "failed" {
		return false, nil
	}
	// Unknown future systemd states conservatively retain the maintenance reservation.
	if !updateUnitActive(state) {
		return false, errors.New("unknown controller update unit state")
	}
	return true, nil
}

// updateUnitActive includes a oneshot service while its installer is still activating.
func updateUnitActive(value string) bool {
	return value == "active" || value == "activating" || value == "reloading" || value == "deactivating"
}
