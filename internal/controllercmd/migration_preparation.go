package controllercmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"hostlier/internal/migrationprep"
	"hostlier/internal/model"
)

// preparationInvocation retains only local preparation options and input/output boundaries.
type preparationInvocation struct {
	command     string
	options     migrationprep.Options
	fingerprint string
	yes, dry    bool
	in          io.Reader
	out, errOut io.Writer
}

// runPreparation keeps prerequisite remediation separate from offline runtime migration.
func runPreparation(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	c, err := parsePreparation(args, in, out, errOut)
	// Help remains a successful, non-mutating invocation.
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	// Syntax errors cannot fall through to privileged inspection.
	if err != nil {
		return err
	}
	// Recovery reads only the original journal, never a newly generated plan.
	if c.command == "status" || c.command == "rollback" {
		return preparationRecovery(ctx, c.command, c.options.BackupDir, c.yes, c.dry, in, out, errOut)
	}
	return c.planOrApply(ctx)
}

// parsePreparation validates the complete command before inspecting any host files.
func parsePreparation(args []string, in io.Reader, out, errOut io.Writer) (*preparationInvocation, error) {
	// An explicit action is required before parsing host options.
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: migrate prepare plan|apply|status|rollback --backup-dir NEW_DIRECTORY")
	}
	c := &preparationInvocation{command: args[0], in: in, out: out, errOut: errOut}
	// Unknown actions cannot select an execution path.
	if !preparationCommand(c.command) {
		return nil, model.Err("ARGUMENTS", "Unknown preparation action")
	}
	f := flag.NewFlagSet("hostlier-controller migrate prepare "+c.command, flag.ContinueOnError)
	f.SetOutput(errOut)
	f.StringVar(&c.options.StateDir, "state-dir", "/var/lib/hostlier", "installed Docker controller state")
	f.StringVar(&c.options.BackupDir, "backup-dir", "", "independent new preparation backup directory")
	f.BoolVar(&c.options.DisableSwap, "disable-swap", false, "review reversible host swap disabling")
	f.BoolVar(&c.options.QuarantineK3s, "quarantine-k3s", false, "quarantine an administrator-reviewed inactive external cluster")
	f.StringVar(&c.fingerprint, "fingerprint", "", "exact separately reviewed fingerprint (required for apply)")
	f.BoolVar(&c.yes, "yes", false, "approve displayed preparation or rollback")
	f.BoolVar(&c.dry, "dry-run", false, "inspect only")
	f.Bool("json", false, "emit JSON (default)")
	// Help and invalid arguments never inspect privileged state.
	if err := f.Parse(args[1:]); err != nil {
		return nil, err
	}
	// A backup directory identifies one independent preparation or retained journal.
	if f.NArg() != 0 || c.options.BackupDir == "" {
		return nil, model.Err("ARGUMENTS", "Preparation requires --backup-dir and no positional arguments")
	}
	return c, nil
}

// preparationCommand enumerates the complete local workflow surface.
func preparationCommand(command string) bool {
	return command == "plan" || command == "apply" || command == "status" || command == "rollback"
}

// planOrApply prints the complete inspection before any explicitly approved prerequisite action.
func (c *preparationInvocation) planOrApply(ctx context.Context) error {
	p, err := migrationprep.Inspect(ctx, c.options)
	// Inspection failure cannot be treated as an executable plan.
	if err != nil {
		return err
	}
	// Display exactly the plan whose fingerprint will be checked at apply.
	if err := json.NewEncoder(c.out).Encode(p); err != nil {
		return err
	}
	// Plan and dry-run modes never mutate host prerequisites.
	if c.command == "plan" || c.dry {
		return nil
	}
	// Broad consent never substitutes for a separately reviewed fingerprint.
	if c.fingerprint == "" || c.fingerprint != p.Fingerprint {
		return model.Err("STALE_PLAN", "Pass the exact --fingerprint from a separate preparation plan")
	}
	// Safety blockers cannot be overridden through approval.
	if len(p.Errors) > 0 {
		return model.Err("PREPARATION_BLOCKED", "Resolve preparation blockers before applying")
	}
	// Preparation approval does not authorize runtime migration.
	if err := approveMigration(c.yes, c.in, c.errOut, nil); err != nil {
		return err
	}
	return migrationprep.Apply(ctx, p, true)
}

// preparationRecovery omits protected fstab bytes while retaining an actionable recovery review.
func preparationRecovery(ctx context.Context, command, dir string, yes, dry bool, in io.Reader, out, errOut io.Writer) error {
	j, err := migrationprep.Read(dir)
	// A missing or corrupt journal cannot authorize host restoration.
	if err != nil {
		return err
	}
	value := map[string]any{"id": j.ID, "phase": j.Phase, "plan": j.Plan, "retainedAssets": j.Assets, "maskedSwapUnits": j.Masks}
	// Status reports recovery locations without printing credential-bearing backup contents.
	if err := json.NewEncoder(out).Encode(value); err != nil {
		return err
	}
	// Status and dry runs leave even unresolved journals untouched.
	if command == "status" || dry {
		return nil
	}
	// Restoring host prerequisites remains a separate explicit approval boundary.
	if err := approveMigration(yes, in, errOut, nil); err != nil {
		return err
	}
	return migrationprep.Rollback(ctx, dir, true)
}
