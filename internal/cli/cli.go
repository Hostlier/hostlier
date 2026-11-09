package cli

import (
	"context"
	"io"

	"hostlier/internal/model"
)

// Run executes a CLI invocation with production dependencies.
func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	return RunWithOptions(ctx, args, in, out, errOut, Options{})
}

// RunWithOptions parses and dispatches one invocation with injectable dependencies.
func RunWithOptions(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, options Options) error {
	parsed, err := parseInvocation(args)
	// Reject malformed arguments, output flags or group actions before reading credentials or contacting a controller.
	if err != nil {
		return err
	}
	s := newSession(ctx, parsed, in, out, errOut, options)
	// Answer help before opening profiles or contacting a controller.
	if s.showHelp() {
		return nil
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.command == "telemetry" {
		return s.telemetryCommand()
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.command == "approve" {
		return s.approveCommand()
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.command == "security" && s.sub == "keygen" {
		return s.securityKeygen()
	}
	// Fleet aliases launch the separate controller executable.
	if s.command == "agent" {
		return s.controllerProcess()
	}
	local := map[string]func() error{"minion": s.minionCommand, "sdk": s.sdk, "ui": s.ui, "skills": s.skills, "validate": s.validate, "server": s.server, "disconnect": s.disconnect, "connect": s.connect, "reauth": s.reauth, "init": s.init, "package": s.packages, "controller": s.localController}
	// Run commands whose work is confined to local configuration or enrollment.
	if handler := local[s.command]; handler != nil {
		return handler()
	}
	return s.projectOrRemoteCommand()
}

// projectOrRemoteCommand lets selected package plans run before controller discovery.
func (s *session) projectOrRemoteCommand() error {
	// Source project commands can select an installed package without a controller profile.
	if projectHookCommand(s.command) {
		handled, err := s.runSelectedPackage()
		// A selected package owns the invocation, including its failure path.
		if handled || err != nil {
			return err
		}
	}
	return s.remoteCommand()
}

// localController forwards daemon operations without linking their implementation.
func (s *session) localController() error {
	// Status and logs continue to use authenticated remote requests.
	switch s.sub {
	case "run": // Run the detached service entry point.
		return s.controllerProcess()
	case "backup", "restore": // Offline recovery owns the writer lock.
		return s.controllerProcess()
	default: // Other controller commands use the ordinary authenticated connection.
		return s.remoteCommand()
	}
}

// remoteCommand enforces approval requirements before opening an authenticated connection.
func (s *session) remoteCommand() error {
	// Reject noninteractive mutation or --yes before contacting the controller.
	if s.requiresApproval() && s.flags["yes"] != "" {
		return s.approve("")
	}
	// Require a usable authenticated connection before dispatching remote commands.
	if err := s.connectRemote(); err != nil {
		return err
	}
	return s.authRecoveryHint(s.dispatch())
}

// dispatch selects an authenticated command handler without mixing command behavior.
func (s *session) dispatch() error {
	// Apply this check before granting authority or advancing protocol state.
	if s.command == "authorize" {
		return s.authorize()
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.command == "authorizations" {
		return s.authorizations()
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.command == "hosts" {
		return s.hosts()
	}
	// Human-security commands use the same authenticated transport but separate passkey authority.
	if s.command == "security" {
		return s.security()
	}
	// Approval observation can resume an interrupted CLI without resubmitting the mutation.
	if s.command == "approval" {
		return s.approval()
	}
	handlers := map[string]func() error{"tunnel": s.tunnelCommand, "fused": s.fused, "backup": s.backup, "ui": s.ui, "operation": s.operation, "scale": s.scale, "sidecar": s.sidecar, "product": s.product, "workflow": s.workflow, "usage": s.usage, "resource": s.resource, "plan": s.deploy, "deploy": s.deploy, "update": s.deploy, "rollback": s.deploy, "list": s.list, "doctor": s.doctor, "status": s.status, "start": s.lifecycle, "stop": s.lifecycle, "restart": s.lifecycle, "delete": s.lifecycle, "logs": s.logs, "controller": s.controllerCommand, "pairing": s.pairing, "clients": s.clients, "secret": s.secret, "domains": s.domains}
	// Dispatch only recognized authenticated commands.
	if handler := handlers[s.command]; handler != nil {
		return handler()
	}
	return model.Err("UNSUPPORTED_COMMAND", "Unsupported command; run hostlier help for supported commands")
}

// requiresApproval identifies commands that reject noninteractive mutation before connecting.
func (s *session) requiresApproval() bool {
	// Limit resource approval gating to provisioning and restore commands.
	if s.command == "resource" {
		return s.sub == "create" || s.sub == "restore"
	}
	return s.deploymentApproval()
}

// deploymentApproval groups commands that change deployment or retained workflow state.
func (s *session) deploymentApproval() bool {
	// Every ordinary deployment or scale needs an interactive review.
	switch s.command {
	case "deploy", "update", "rollback", "scale", "sidecar": // Deployment variants share the approval boundary.
		return true
	case "product": // Product planning is read-only.
		return s.sub == "deploy"
	case "workflow": // Inspecting recovery state is read-only.
		return s.sub == "reconcile"
	}
	return false
}

// parseInvocation parses flags, rejects conflicting output formats and routes group actions.
func parseInvocation(args []string) (arguments, error) {
	parsed, err := parse(args)
	// Malformed command arguments never create invocation state.
	if err != nil {
		return parsed, err
	}
	// Conflicting output formats are rejected before any work starts.
	if err = validateOutputFlags(parsed.flags); err != nil {
		return parsed, err
	}
	parsed, err = routeArguments(parsed)
	// Validate the normalized command so grouped app deploy and deploy accept the same flags.
	if err != nil {
		return parsed, err
	}
	if parsed.flags["all"] != "" && (len(parsed.args) == 0 || parsed.args[0] != "list") {
		return parsed, model.Err("ARGUMENTS", "--all requires hostlier list or hostlier app list")
	}
	// Only local project lifecycle commands can claim a package selection.
	if _, ok := parsed.flags["package"]; ok && (len(parsed.args) == 0 || (parsed.args[0] != "init" && !projectHookCommand(parsed.args[0]))) {
		return parsed, model.Err("ARGUMENTS", "--package is supported by init, plan, deploy and update")
	}
	return parsed, nil
}
