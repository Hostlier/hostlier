package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"hostlier/internal/controllerbinary"
	"hostlier/internal/model"
)

// validateApprovedInstallation rejects altered plans and changed server conditions.
func validateApprovedInstallation(ctx context.Context, p Plan, o ApplyOptions) error {
	// Require explicit approval before inspecting or mutating installation state.
	if !o.Approved {
		return model.Err("APPROVAL_REQUIRED", "Initialization requires explicit approval")
	}
	// An unsupported preview cannot authorize installation.
	if !p.Supported {
		return model.Err("UNSUPPORTED_INSTALLATION", strings.Join(p.Errors, " "))
	}
	// Only the supported privileged Linux execution environment may apply a plan.
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return model.Err("UNSUPPORTED_INSTALLATION", "Apply only as root on a supported Ubuntu server")
	}
	// Reject edits to any fingerprinted installation field.
	if fingerprint(p) != p.Fingerprint {
		return model.Err("STALE_PLAN", "Installation plan was altered")
	}
	current, e := Inspect(ctx, p.Options)
	// Abort if fresh inspection cannot establish current server conditions.
	if e != nil {
		return e
	}
	// Require a new approval when the host changed after inspection.
	if !reflect.DeepEqual(current, p) {
		return model.Err("STALE_PLAN", "Server conditions changed; inspect and approve a new plan")
	}
	return nil
}

// installationRunner owns an approved installation and its durable progress.
type installationRunner struct {
	runOverride         func(string, ...string) error
	caddyBinaryOverride func(context.Context, bool) error
	ctx                 context.Context
	plan                Plan
	options             ApplyOptions
	assets              Assets
	progress            Installation
}

// prepare records approved ownership before any package service can start.
func (a *installationRunner) prepare() error {
	p := a.plan
	// Carry forward only recorded ownership and firewall progress on retry.
	if p.Snapshot.Installation != nil {
		a.progress.OwnedCaddy = p.Snapshot.Installation.OwnedCaddy
		a.progress.OwnedNginx = p.Snapshot.Installation.OwnedNginx
		a.progress.FirewallRules = p.Snapshot.Installation.FirewallRules
	}
	// Create protected progress storage before package installation.
	if err := os.MkdirAll(p.StateDir, 0700); err != nil {
		return err
	}
	// Record optional Nginx ownership before its newly installed package can start.
	if p.Proxy == "nginx" && !p.Snapshot.Nginx.Installed {
		a.progress.OwnedNginx = true
	}
	// Record approved ownership before a new Caddy package can start its default service.
	if p.Proxy == "caddy" && !p.Snapshot.Caddy.Installed {
		a.progress.OwnedCaddy = p.Proxy == "caddy"
	}
	return a.persist()
}

// persist commits completed installation steps for safe retries.
func (a *installationRunner) persist() error {
	data, _ := json.Marshal(a.progress)
	return atomic(filepath.Join(a.plan.StateDir, "installation.json"), data, 0600)
}

// run executes an approved installation action and reports bounded failures.
func (a *installationRunner) run(file string, args ...string) error {
	// Tests can audit exact service mutations without executing privileged host commands.
	if a.runOverride != nil {
		return a.runOverride(file, args...)
	}
	ctx := a.ctx
	_ = ctx

	stepCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(stepCtx, file, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l")
	// Hide command output that could include sensitive configuration while retaining the failing executable.
	if e := cmd.Run(); e != nil {
		return model.Err("INSTALL_STEP_FAILED", "Installation command failed: "+file+"; progress retained for retry")
	}
	return nil
}

// step executes an approved installation action and reports bounded failures.
func (a *installationRunner) step(name string, fn func() error) error {
	a.report(name, "started")
	var done chan struct{}
	var stopped chan struct{}
	if a.options.OnProgress != nil {
		done = make(chan struct{})
		stopped = make(chan struct{})
		go func() {
			defer close(stopped)
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					select {
					case <-done:
						return
					default:
						a.report(name, "waiting")
					}
				}
			}
		}()
	}
	stopWaiting := func() {
		if done != nil {
			close(done)
			<-stopped
		}
	}
	// Keep the underlying cause so an operator can resolve a failed stage without guessing.
	e := fn()
	stopWaiting()
	if e != nil {
		a.report(name, "failed")
		return model.Err("INSTALL_STEP_FAILED", fmt.Sprintf("%s failed: %v; completed steps: %s. Retry init after resolving the cause.", name, e, strings.Join(a.progress.Completed, ", ")))
	}
	a.progress.Completed = append(a.progress.Completed, name)
	if err := a.persist(); err != nil {
		a.report(name, "failed")
		return err
	}
	a.report(name, "completed")
	return nil
}

// report sends live stage status without changing durable installation state.
func (a *installationRunner) report(step, status string) {
	if a.options.OnProgress != nil {
		a.options.OnProgress(step, status)
	}
}

// packages installs only missing approved packages and cluster firewall tools.
func (a *installationRunner) packages() error {
	return a.installPackages()
}

// installPackages provisions dependencies only when Hostlier owns the selected infrastructure.
func (a *installationRunner) installPackages() error {
	p := a.plan
	run := a.run

	packages := nginxGatewayPackages(p)
	// Install Docker only for a Docker plan that has no supported package yet.
	if installDockerPackage(p) {
		packages = append(packages, "docker.io="+dockerPackageVersion(p))
	}
	// Reuse an existing supported Caddy package.
	if p.Proxy == "caddy" && !p.Snapshot.Caddy.Installed {
		packages = append(packages, "caddy="+caddyPackageVersion(p))
	}
	// Install optional Nginx and its ACME client only after explicit selection.
	if p.Proxy == "nginx" {
		// Stop activation when the required local operation fails.
		if err := a.nginxPackages(); err != nil {
			return err
		}
	}
	// The cluster firewall guard requires the iptables tools.
	if p.Runtime == "k3s" {
		packages = append(packages, "iptables")
	}
	// Avoid package-manager mutations when all required packages already exist.
	if len(packages) > 0 {
		// Refresh package metadata before installing exact supported versions.
		if e := run("apt-get", "update"); e != nil {
			return e
		}
		if e := run("apt-get", append([]string{"install", "-y", "--no-remove", "--no-install-recommends", "logrotate", "ca-certificates"}, packages...)...); e != nil {
			return e
		}
	}
	if p.Proxy == "caddy" || p.Proxy == "nginx-gateway" {
		owned := p.Snapshot.Installation != nil && p.Snapshot.Installation.OwnedCaddy
		if a.caddyBinaryOverride != nil {
			return a.caddyBinaryOverride(a.ctx, owned)
		}
		return installCaddyBinary(a.ctx, owned)
	}
	return nil
}

// configuration commits approved configuration assets and the controller executable.
func (a *installationRunner) configuration() error {
	p := a.plan
	assets := a.assets
	progress := &a.progress

	// Establish protected configuration directories before writing assets.
	if err := prepareConfigurationDirectoriesFor(p.Proxy); err != nil {
		return err
	}
	// Preserve an existing server configuration across retries.
	if _, e := os.Stat(p.ConfigPath); os.IsNotExist(e) {
		// Do not continue if the initial configuration cannot be committed.
		if e = atomic(p.ConfigPath, []byte(assets.Config), 0600); e != nil {
			return e
		}
	}
	// Write the selected edge assets through its dedicated ownership path.
	if err := a.edgeConfiguration(); err != nil {
		return err
	}

	// Do not mark ownership complete until the controller executable is installed.
	if err := a.installExecutable(); err != nil {
		return err
	}
	progress.OwnedCaddy = p.Proxy == "caddy" || p.Proxy == "nginx-gateway"
	return nil
}

// firewall adds only approved web rules and scoped cluster pod paths.
func (a *installationRunner) firewall() error {
	// Existing firewall policy is never mutated by coexistence, even on a resumed installation.
	if a.plan.Proxy == "nginx-existing" || externalDocker(a.plan.Options) {
		return nil
	}
	p := a.plan
	progress := &a.progress
	run := a.run
	persist := a.persist

	// Apply only the web rules shown in the approved firewall preview.
	for _, port := range p.Snapshot.Rules {
		// A failed allow rule must not be recorded as installed.
		if e := run("ufw", "allow", fmt.Sprint(port)+"/tcp", "comment", "hostlier-web"); e != nil {
			return e
		}
		progress.FirewallRules = append(progress.FirewallRules, port)
		// Persist each rule immediately so retries retain accurate progress.
		if e := persist(); e != nil {
			return e
		}
	}
	// Open only the cluster pod paths required when UFW manages the host firewall.
	if p.Runtime == "k3s" && p.Snapshot.Firewall == "ufw" {
		// Scope cluster forwarding rules to cni0 and the fixed pod subnet.
		for _, args := range [][]string{{"allow", "in", "on", "cni0", "from", "10.42.0.0/16", "comment", "hostlier-k3s-pods"}, {"route", "allow", "in", "on", "cni0", "from", "10.42.0.0/16", "comment", "hostlier-k3s-pods"}, {"route", "allow", "out", "on", "cni0", "to", "10.42.0.0/16", "comment", "hostlier-k3s-pods"}} {
			// Stop before claiming firewall readiness if any scoped rule failed.
			if e := run("ufw", args...); e != nil {
				return e
			}
		}
	}
	return nil
}

// services loads units and starts the approved runtime, edge and controller.
func (a *installationRunner) services() error {
	p := a.plan
	run := a.run

	// Reload service definitions before enabling newly written units.
	if e := run("systemctl", "daemon-reload"); e != nil {
		return e
	}
	// Only Hostlier's controller and its isolated renewal timer may be enabled or restarted.
	if p.Proxy == "nginx-existing" {
		return a.sharedNginxServices()
	}
	// Runtime lifecycle follows its ownership independently of which proxy will start.
	if err := a.runtimeService(); err != nil {
		return err
	}
	// Start the standalone Nginx service only for an explicitly selected Nginx plan.
	if p.Proxy == "nginx" {
		return a.nginxServices()
	}
	// The gateway has its own private backend service and retains Caddy for public protocols.
	if p.Proxy == "nginx-gateway" {
		return a.nginxGatewayServices()
	}
	// Enable all required services and start them immediately.
	if e := run("systemctl", "enable", "--now", "caddy.service", "hostlier.service"); e != nil {
		return e
	}
	// Reload the approved Caddy unit instead of retaining the package default process.
	if e := run("systemctl", "restart", "caddy.service"); e != nil {
		return e
	}
	// enable --now does not activate a replaced binary on an existing service.
	if e := run("systemctl", "restart", "hostlier.service"); e != nil {
		return e
	}
	return run("systemctl", "is-active", "hostlier.service", "caddy.service", p.Runtime+".service")
}

// installDockerPackage requires both a selected managed runtime and an absent package.
func installDockerPackage(p Plan) bool {
	return p.Runtime == "docker" && !externalDocker(p.Options) && !p.Snapshot.Docker.Installed
}

// runtimeService starts only Hostlier-owned infrastructure and merely checks external Docker.
func (a *installationRunner) runtimeService() error {
	// Reuse never changes the external daemon's boot policy or running state.
	if externalDocker(a.plan.Options) {
		return a.run("systemctl", "is-active", "docker.service")
	}
	return a.run("systemctl", "enable", "--now", a.plan.Runtime+".service")
}

// controllerReadiness waits for the private Unix-socket health endpoint.
func (a *installationRunner) controllerReadiness() error {
	ctx := a.ctx

	// Probe administration through the private Unix socket rather than public HTTPS.
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/run/hostlier/controller.sock")
	}}}
	deadline := time.Now().Add(30 * time.Second)
	// Bound local controller readiness independently of installation commands.
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://local/health", nil)
		res, e := client.Do(req)
		// Inspect status only when the local HTTP request completed.
		if e == nil {
			res.Body.Close()
			// A healthy local controller is required before recording readiness.
			if res.StatusCode == 200 {
				return nil
			}
		}
		// Wait between probes while preserving cancellation responsiveness.
		select {
		// Honor cancellation before another readiness request.
		case <-ctx.Done():
			return ctx.Err()
		// Retry after a short delay to avoid a busy polling loop.
		case <-time.After(300 * time.Millisecond):
		}
	}
	return model.Err("CONTROLLER_NOT_READY", "Controller not locally ready; inspect journalctl -u hostlier.service")
}

// k3s installs the pinned cluster only for the approved k3s runtime.
func (a *installationRunner) k3s() error {
	// Skip cluster installation for Docker plans.
	if a.plan.Runtime != "k3s" {
		return nil
	}
	return installK3s(a.ctx, a.plan, a.run)
}

// edgeReadiness verifies the approved edge configuration is active.
func (a *installationRunner) edgeReadiness() error {
	// Both routing layers must be live before a managed gateway installation succeeds.
	if a.plan.Proxy == "nginx-gateway" {
		return a.nginxGatewayReadiness()
	}
	// A completed retry must not replace routes already published by the controller.
	if a.plan.Proxy == "nginx-existing" {
		return a.sharedNginxReadiness()
	}
	// Nginx issues certificates using its owned HTTP challenge listener.
	if a.plan.Proxy == "nginx" {
		return a.nginxReadiness()
	}
	return ensureEdge(a.ctx, a.plan, a.assets.Caddy)
}

// installExecutable installs only the controller from the reviewed server release.
func (a *installationRunner) installExecutable() error {
	source := a.options.SourceExecutable
	// Never install the CLI itself as the controller when a source was omitted.
	if source == "" {
		var err error
		source, err = controllerbinary.Resolve()
		// Missing release artifacts must fail before replacing an installed executable.
		if err != nil {
			return err
		}
	}
	return copyExecutable(source, a.plan.Executable)
}

// copyExecutable atomically activates a completely written executable.
func copyExecutable(source, destination string) error {
	// Avoid copying an installed executable over itself.
	if source == destination {
		return nil
	}
	in, err := os.Open(source)
	// Preserve source errors before creating a staging file.
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(destination), ".hostlier-executable-*")
	// Private staging avoids following an existing temporary-file symlink.
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	// Apply executable permissions before publishing the artifact.
	if err = out.Chmod(0755); err != nil {
		return err
	}
	// Partial copies must never replace a working binary.
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	// Flush bytes before activation so write failures are reported.
	if err = out.Sync(); err != nil {
		return err
	}
	// A close failure also prevents publishing the staged executable.
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), destination)
}

// prepareConfigurationDirectories establishes the approved file ownership boundaries.
func prepareConfigurationDirectories() error {
	return prepareConfigurationDirectoriesFor("caddy")
}

// prepareConfigurationDirectoriesFor touches the Caddy drop-in only when installing that proxy.
func prepareConfigurationDirectoriesFor(proxy string) error {
	directories := []string{"/etc/hostlier", "/var/log/hostlier"}
	// Nginx coexistence leaves all other proxy configuration directories unchanged.
	if proxy == "caddy" || proxy == "nginx-gateway" {
		directories = append(directories, "/etc/systemd/system/caddy.service.d")
	}
	// Create each configuration directory without broadening unrelated permissions.
	for _, dir := range directories {
		// Stop before writing files into an unavailable directory.
		if e := os.MkdirAll(dir, 0700); e != nil {
			return e
		}
	}
	// Allow Caddy to traverse its configuration directory.
	if e := os.Chmod("/etc/hostlier", 0755); e != nil {
		return e
	}
	// Allow systemd to read its approved Caddy drop-in.
	if proxy == "caddy" || proxy == "nginx-gateway" {
		if e := os.Chmod("/etc/systemd/system/caddy.service.d", 0755); e != nil { // Avoid touching configuration directories belonging to another proxy.
			return e
		}
	}
	return nil
}
