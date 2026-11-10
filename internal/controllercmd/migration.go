package controllercmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"hostlier/internal/migration"
	"hostlier/internal/model"
)

// migrationDependencies retains the typed inputs required by this migration step.
type migrationDependencies struct {
	inspect  func(context.Context, migration.Options) (migration.Plan, error)
	apply    func(context.Context, migration.Plan, bool, func(string)) error
	rollback func(context.Context, string, bool, func(string)) error
	read     func(string) (*migration.Journal, error)
	confirm  func() (bool, error)
}

// migrationInvocation retains only reviewed local command options and IO.
type migrationInvocation struct {
	command              string
	options              migration.Options
	to                   string
	yes, dry, jsonOutput bool
	deps                 migrationDependencies
	in                   io.Reader
	out, errOut          io.Writer
}

// runMigration parses and previews before invoking any privileged execution path.
func runMigration(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, deps migrationDependencies) error {
	c, err := parseMigration(args, in, out, errOut, deps)
	// Preserve failure before advancing to the next external-effect boundary.
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if c.command == "status" || c.command == "rollback" {
		return c.statusOrRollback(ctx)
	}
	return c.planOrApply(ctx)
}

// parseMigration rejects unsupported direction and positional arguments without reading state.
func parseMigration(args []string, in io.Reader, out, errOut io.Writer, deps migrationDependencies) (*migrationInvocation, error) {
	// Reject incompatible state before continuing the protected migration step.
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: migrate plan|apply|status|rollback [--to k3s] [--backup-dir NEW_DIRECTORY]")
	}
	c := &migrationInvocation{command: args[0], in: in, out: out, errOut: errOut, deps: deps}
	// Reject incompatible state before continuing the protected migration step.
	if !migrationSubcommand(c.command) {
		return nil, fmt.Errorf("unsupported migration command %q", c.command)
	}
	flags := flag.NewFlagSet("hostlier-controller migrate "+c.command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.StringVar(&c.options.StateDir, "state-dir", "/var/lib/hostlier", "original controller state directory")
	flags.StringVar(&c.options.Config, "config", "/etc/hostlier/config.yaml", "installed controller configuration")
	flags.StringVar(&c.options.BackupDir, "backup-dir", "", "new private backup directory outside controller state")
	flags.BoolVar(&c.options.Retry, "retry", false, "review a new migration after a completed rollback, retaining previous backups and volumes")
	flags.StringVar(&c.to, "to", "k3s", "target runtime (k3s)")
	flags.BoolVar(&c.yes, "yes", false, "approve the displayed migration or rollback")
	flags.BoolVar(&c.dry, "dry-run", false, "preview only")
	flags.BoolVar(&c.jsonOutput, "json", false, "emit JSON")
	// Preserve failure before advancing to the next external-effect boundary.
	if err := flags.Parse(args[1:]); err != nil {
		return nil, err
	}
	// Reject incompatible state before continuing the protected migration step.
	if flags.NArg() != 0 || c.to != "k3s" {
		return nil, model.Err("ARGUMENTS", "Migration supports Docker to k3s only")
	}
	return c, nil
}

// migrationSubcommand enumerates the complete local workflow surface.
func migrationSubcommand(command string) bool {
	// Select only representations explicitly supported by the migration contract.
	switch command {
	// Keep this alternative within the validated operation or archive contract.
	case "plan", "apply", "status", "rollback":
		return true
	// Keep this alternative within the validated operation or archive contract.
	default:
		return false
	}
}

// progress keeps machine-readable plan output separate from execution updates.
func (c *migrationInvocation) progress(phase string) {
	fmt.Fprintln(c.errOut, "Hostlier migration:", phase)
}

// statusOrRollback reports only public journal fields before requesting approval.
func (c *migrationInvocation) statusOrRollback(ctx context.Context) error {
	// Treat absent optional state explicitly rather than inferring authority.
	if c.deps.read == nil {
		c.deps.read = migration.Read
	}
	j, err := c.deps.read(c.options.StateDir)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	result := map[string]any{"id": j.ID, "phase": j.Phase, "from": "docker", "to": "k3s", "backupDirectory": j.Plan.BackupDir, "applications": j.Plan.Applications, "volumes": len(j.Plan.Volumes), "preserveCurrentTargetData": j.PreserveTarget}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := json.NewEncoder(c.out).Encode(result); err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if c.command == "status" || c.dry {
		return nil
	}
	fmt.Fprintln(c.out, "Rollback stops both runtimes, backs up current Kubernetes volume contents, restores those contents to Docker, verifies the original workloads, and retains every backup. Later deployment changes block rollback.")
	// Preserve failure before advancing to the next external-effect boundary.
	if err := approveMigration(c.yes, c.in, c.errOut, c.deps.confirm); err != nil {
		return err
	}
	// Treat absent optional state explicitly rather than inferring authority.
	if c.deps.rollback == nil {
		c.deps.rollback = migration.Rollback
	}
	return c.deps.rollback(ctx, c.options.StateDir, true, c.progress)
}

// planOrApply enforces the blocker and approval gates after displaying the exact plan.
func (c *migrationInvocation) planOrApply(ctx context.Context) error {
	// Reject incompatible state before continuing the protected migration step.
	if c.options.BackupDir == "" {
		return model.Err("ARGUMENTS", "Migration plan/apply requires --backup-dir NEW_ABSOLUTE_DIRECTORY")
	}
	// Treat absent optional state explicitly rather than inferring authority.
	if c.deps.inspect == nil {
		c.deps.inspect = migration.Inspect
	}
	p, err := c.deps.inspect(ctx, c.options)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := c.printMigrationPlan(p); err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if c.command == "plan" || c.dry {
		return nil
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(p.Errors) > 0 {
		return model.Err("MIGRATION_PREFLIGHT", "Resolve migration blockers before applying")
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := approveMigration(c.yes, c.in, c.errOut, c.deps.confirm); err != nil {
		return err
	}
	// Treat absent optional state explicitly rather than inferring authority.
	if c.deps.apply == nil {
		c.deps.apply = migration.Apply
	}
	return c.deps.apply(ctx, p, true, c.progress)
}

// printMigrationPlan writes one complete preview and propagates output failures.
func (c *migrationInvocation) printMigrationPlan(p migration.Plan) error {
	// Reject incompatible state before continuing the protected migration step.
	if c.jsonOutput {
		return json.NewEncoder(c.out).Encode(p)
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "Docker → k3s migration\nApplications: %v\nVolumes: %d\nBackups: %s\n", p.Applications, len(p.Volumes), p.BackupDir)
	// Complete every selected item before advancing the migration boundary.
	for _, change := range p.Changes {
		fmt.Fprintln(&out, "  -", change)
	}
	// Complete every selected item before advancing the migration boundary.
	for _, problem := range p.Errors {
		fmt.Fprintln(&out, "BLOCKED:", problem)
	}
	fmt.Fprintln(&out, "Plan fingerprint:", p.Fingerprint)
	_, err := c.out.Write(out.Bytes())
	return err
}

// approveMigration keeps this migration step within its validated recovery boundary.
func approveMigration(yes bool, in io.Reader, out io.Writer, confirm func() (bool, error)) error {
	// Reject incompatible state before continuing the protected migration step.
	if yes {
		return nil
	}
	// Treat absent optional state explicitly rather than inferring authority.
	if confirm == nil {
		// Keep the callback bound to the current migration and its checkpoint.
		confirm = func() (bool, error) { return confirmInstallation(in, out) }
	}
	approved, err := confirm()
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if !approved {
		return model.Err("CANCELLED", "Cancelled; no changes applied")
	}
	return nil
}
