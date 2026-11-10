// Package controllercmd owns daemon startup and offline controller recovery.
package controllercmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// arguments retains the positional command used by pinned fleet startup.
type arguments struct{ args []string }

// session contains only local daemon configuration and output.
type session struct {
	ctx          context.Context
	flags        map[string]string
	command, sub string
	parsed       arguments
	out          io.Writer
}

const helpText = `Hostlier controller
Usage: hostlier-controller run [options]
       hostlier-controller connect MASTER_URL_OR_PROFILE
       hostlier-controller init --domain DOMAIN [--master-fingerprint SHA256] [--dry-run] [--yes] [--runtime auto|docker|k3s] [--proxy MODE]
       hostlier-controller update [--dry-run] [--yes] [--state-dir DIR] [--config FILE]
       hostlier-controller migrate plan|apply --to k3s --backup-dir NEW_DIRECTORY [--yes]
       hostlier-controller migrate status|rollback [--state-dir DIR] [--yes]
       hostlier-controller migrate prepare plan|apply --backup-dir NEW_DIRECTORY --disable-swap --quarantine-k3s [--fingerprint SHA256] [--yes]
       hostlier-controller migrate prepare status|rollback --backup-dir DIRECTORY [--yes]
       hostlier-controller bootstrap --url HTTPS_ORIGIN --request ID [--socket PATH --name OWNER]
       hostlier-controller agent run --controller-fingerprint SHA256 [options]
       hostlier-controller backup --output FILE [--state-dir DIR --config FILE]
       hostlier-controller restore --input FILE --source-stopped --state-dir DIR
Options: --state-dir --config --runtime --proxy --hostname --controller-hostname
         --host --port --socket --log-path --auth-config --data-config
         --controller-fingerprint --tunnel-domain --tunnel-url
`

// Run dispatches local server operations without importing the user CLI or dashboard.
func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	args = controllerArguments(args)
	// Help never opens server configuration or persistent state.
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := fmt.Fprint(out, helpText)
		return err
	}
	// Setup commands have their own flags and never enter daemon parsing.
	if handled, err := runLocalSetup(ctx, args, in, out, errOut); handled {
		return err
	}

	s := &session{ctx: ctx, out: out, flags: map[string]string{}, command: "controller", sub: args[0]}
	rest := args[1:]
	// Fleet hosts use the same runtime with a pinned controller identity.
	if s.sub == "agent" {
		s.command = "agent"
		// Require the fleet subcommand before parsing daemon flags.
		if len(rest) == 0 {
			return fmt.Errorf("agent requires run")
		}
		s.sub, rest = rest[0], rest[1:]
		s.parsed.args = []string{"agent", s.sub}
	}
	// Validate every daemon option before reading configuration or opening state.
	if err := s.parse(rest, errOut); err != nil {
		// Standard flag help is a successful read-only invocation.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return s.execute()
}

// runLocalSetup routes installation and migration without adding daemon flags.
func runLocalSetup(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) (bool, error) {
	// Select only representations explicitly supported by the migration contract.
	switch args[0] {
	// Keep this alternative within the validated operation or archive contract.
	case "connect":
		return true, connectMaster(ctx, args[1:], in, out, errOut)
	// Keep this alternative within the validated operation or archive contract.
	case "init":
		return true, runInit(ctx, args[1:], in, out, errOut, initDependencies{})
	// Keep this alternative within the validated operation or archive contract.
	case "update":
		return true, runUpdate(ctx, args[1:], in, out, errOut)
	// Only the root-owned detached unit invokes this fixed-purpose execution entry point.
	case "maintenance-run":
		return true, runControllerUpdate(ctx, args[1:], errOut)
	// Keep this alternative within the validated operation or archive contract.
	case "migrate":
		return true, runMigration(ctx, args[1:], in, out, errOut, migrationDependencies{})
	// Keep this alternative within the validated operation or archive contract.
	default:
		return false, nil
	}
}

// execute keeps the daemon command surface limited to locally owned operations.
func (s *session) execute() error {
	// Fleet startup additionally validates the required peer pin.
	if s.command == "agent" {
		return s.runAgent()
	}
	// Remote administration belongs to the client executable.
	switch s.sub {
	case "bootstrap": // Use the running controller's protected socket for owner setup.
		return s.bootstrapOwner()
	case "run": // Start the local service until cancellation.
		return s.runController()
	case "backup", "restore": // Recovery requires exclusive ownership of controller state.
		return s.controllerRecovery()
	default: // Do not fall through into remote CLI commands.
		return fmt.Errorf("unknown controller command %q; run hostlier-controller help", s.sub)
	}
}

// parse registers only controller flags, rejecting unrelated CLI options and extra positionals.
func (s *session) parse(args []string, errOut io.Writer) error {
	flags := flag.NewFlagSet("hostlier-controller", flag.ContinueOnError)
	flags.SetOutput(errOut)
	values := map[string]*string{}
	// Each value is loaded into the daemon session after successful parsing.
	for _, name := range strings.Fields("state-dir config runtime proxy hostname controller-hostname host port socket log-path auth-config data-config controller-fingerprint master-fingerprint output input tunnel-domain tunnel-url url request name") {
		values[name] = flags.String(name, "", name)
	}
	flags.Bool("json", false, "emit JSON status (default)")
	stopped := flags.Bool("source-stopped", false, "original controller is stopped")
	// Parsing errors must not open controller state.
	if err := flags.Parse(args); err != nil {
		return err
	}
	// Unexpected positionals usually indicate a misspelled command or option.
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected controller arguments: %v", flags.Args())
	}
	// Copy parsed values without retaining flag package state.
	for name, value := range values {
		s.flags[name] = *value
	}
	// Restore requires an explicit positive assertion.
	if *stopped {
		s.flags["source-stopped"] = "true"
	}
	return nil
}

// print writes one machine-readable daemon status record.
func (s *session) print(value any) error { return json.NewEncoder(s.out).Encode(value) }

// defaultValue supplies the installed default only when the operator omitted a value.
func defaultValue(value, fallback string) string {
	// Preserve explicitly selected configuration.
	if value != "" {
		return value
	}
	return fallback
}

// controllerArguments retains legacy CLI forwarding for existing daemon and recovery commands.
func controllerArguments(args []string) []string {
	// Strip only the explicit historical controller prefix.
	if len(args) > 0 && args[0] == "controller" {
		return args[1:]
	}
	return args
}
