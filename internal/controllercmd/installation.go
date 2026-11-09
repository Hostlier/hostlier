package controllercmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
	"hostlier/internal/installer"
	"hostlier/internal/model"
)

// initDependencies isolates privileged installation from command parsing and approval tests.
type initDependencies struct {
	inspect    func(context.Context, installer.Options) (installer.Plan, error)
	apply      func(context.Context, installer.Plan, installer.ApplyOptions) error
	confirm    func() (bool, error)
	executable string
}

// runInit previews the same server installation plan before any privileged mutation.
func runInit(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, deps initDependencies) error {
	flags := flag.NewFlagSet("hostlier-controller init", flag.ContinueOnError)
	flags.SetOutput(errOut)
	options := installer.Options{}
	flags.StringVar(&options.MasterFingerprint, "master-fingerprint", "", "pin the master controller on the dedicated fleet listener")
	flags.StringVar(&options.Domain, "domain", "", "server application domain")
	flags.StringVar(&options.Runtime, "runtime", "", "auto, docker or k3s")
	flags.StringVar(&options.Proxy, "proxy", "", "auto, caddy, nginx, nginx-existing or nginx-gateway")
	flags.StringVar(&options.ControllerHostname, "controller-hostname", "", "controller hostname")
	flags.StringVar(&options.StateDir, "state-dir", "", "controller state directory")
	flags.StringVar(&options.ConfigPath, "config", "", "controller configuration path")
	dry := flags.Bool("dry-run", false, "preview without changing the server")
	yes := flags.Bool("yes", false, "approve the displayed server installation")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	// Help is successful and never opens server state.
	if err := flags.Parse(args); err != nil {
		// Standard flag help is a read-only success.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	// Unexpected positionals must not be interpreted as installation targets.
	if flags.NArg() != 0 {
		return model.Err("ARGUMENTS", "Server init accepts flags only")
	}
	executable, err := initExecutable(deps.executable)
	// Resolve the controller payload before assembling its collector locations.
	if err != nil {
		return err
	}
	options.CollectorSource = installer.CollectorSource(executable, defaultValue(options.StateDir, "/var/lib/hostlier"))
	// Production inspection is read-only; tests supply deterministic host facts.
	if deps.inspect == nil {
		deps.inspect = installer.Inspect
	}
	progress := installationProgress(errOut, *jsonOutput)
	progress("inspection", "started")
	plan, err := deps.inspect(ctx, options)
	// Failed inspection cannot reach the installation approval gate.
	if err != nil {
		progress("inspection", "failed")
		return err
	}
	progress("inspection", "completed")
	// The operator must receive the exact plan before approving any work.
	if err = writeInstallationPlan(out, plan, *jsonOutput); err != nil {
		return err
	}
	// Preview never requires a CLI sibling or privileges to mutate the host.
	if *dry {
		return nil
	}
	return installReviewed(ctx, plan, executable, *yes, *jsonOutput, in, out, errOut, deps)
}

// writeInstallationPlan puts blockers first while retaining the full plan for --json callers.
func writeInstallationPlan(out io.Writer, plan installer.Plan, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(out).Encode(plan)
	}
	status := "Ready to install"
	if !plan.Supported {
		status = "Installation blocked"
	}
	if _, err := fmt.Fprintf(out, "Hostlier controller installation: %s\nController: https://%s\nDomain: %s\nRuntime: %s\nProxy: %s\n", status, plan.ControllerHostname, plan.Domain, plan.Runtime, plan.Proxy); err != nil {
		return err
	}
	for _, section := range []struct {
		label string
		items []string
	}{
		{"Errors to resolve", plan.Errors},
		{"Proposed changes", plan.Changes},
		{"Warnings", plan.Warnings},
	} {
		if len(section.items) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(out, "\n%s:\n", section.label); err != nil {
			return err
		}
		for _, item := range section.items {
			if _, err := fmt.Fprintf(out, "  - %s\n", item); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(out, "\nPlan fingerprint: %s\n", plan.Fingerprint)
	return err
}

// initExecutable follows the running controller's real path so symlink installations find sibling assets.
func initExecutable(override string) (string, error) {
	// Tests provide a harmless payload location without resolving their test binary.
	if override != "" {
		return override, nil
	}
	executable, err := os.Executable()
	// A missing process identity cannot be substituted with a guessed binary.
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(executable)
}

// installReviewed preserves the supported-host and explicit approval gates from server setup.
func installReviewed(ctx context.Context, plan installer.Plan, executable string, yes, jsonOutput bool, in io.Reader, out, errOut io.Writer, deps initDependencies) error {
	// An affirmative answer cannot override a failed platform or coexistence preflight.
	if !plan.Supported {
		return model.Err("INSTALL_PREFLIGHT", "Installation preflight failed; resolve the reported conflicts")
	}
	// Noninteractive installations require an explicit --yes at this command boundary.
	if !yes {
		confirm := deps.confirm
		// Ordinary command use requires a real terminal before reading approval input.
		if confirm == nil {
			// Bind the current terminal streams without exposing installation state.
			confirm = func() (bool, error) { return confirmInstallation(in, errOut) }
		}
		approved, err := confirm()
		// Input failures and declined reviews must leave the server unchanged.
		if err != nil {
			return err
		}
		// Declining is never implicit approval.
		if !approved {
			return model.Err("CANCELLED", "Cancelled; no changes applied")
		}
	}
	// The server release installs its controller without requiring a client binary.
	if deps.apply == nil {
		deps.apply = installer.Apply
	}
	progress := installationProgress(errOut, jsonOutput)
	err := deps.apply(ctx, plan, installer.ApplyOptions{Approved: true, SourceExecutable: executable, OnProgress: progress})
	// Success is reported only after the installer completes all reviewed work.
	if err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(out).Encode(map[string]bool{"installed": true})
	}
	_, err = fmt.Fprintf(out, "\nHostlier controller installed at https://%s\n", plan.ControllerHostname)
	return err
}

// installationProgress writes live status to stderr so stdout stays a plan or result stream.
func installationProgress(errOut io.Writer, jsonOutput bool) func(string, string) {
	return func(step, status string) {
		if jsonOutput {
			_ = json.NewEncoder(errOut).Encode(map[string]string{"step": step, "status": status})
			return
		}
		label := map[string]string{
			"inspection": "Inspecting server", "validation": "Rechecking approved plan",
			"preparation": "Preparing installation state", "collector-image": "Loading collector image",
			"packages": "Installing dependencies", "configuration": "Writing configuration",
			"firewall": "Checking firewall", "k3s": "Setting up Kubernetes",
			"services": "Starting services", "edge-readiness": "Configuring edge and TLS",
			"controller-readiness": "Checking controller", "collector-readiness": "Checking collector",
			"finalization": "Saving installation result",
		}[step]
		if label == "" {
			label = step
		}
		_, _ = fmt.Fprintf(errOut, "Hostlier controller: %s — %s\n", label, status)
	}
}

// confirmInstallation prevents piped input from accidentally authorizing host changes.
func confirmInstallation(in io.Reader, out io.Writer) (bool, error) {
	file, ok := in.(*os.File)
	// Headless callers must deliberately supply --yes after inspecting --dry-run output.
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return false, model.Err("INTERACTIVE_REQUIRED", "Review with --dry-run, then use a terminal or explicit --yes")
	}
	fmt.Fprint(out, "Apply these server changes? [y/N] ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	// Interrupted input remains a cancellation rather than a partial affirmative response.
	if err != nil {
		return false, err
	}
	answer = strings.TrimSpace(answer)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}
