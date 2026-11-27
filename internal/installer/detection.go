package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// hostDetection records presence separately from compatibility; incompatible infrastructure is never replaced.
type hostDetection struct {
	Docker, Nginx bool
}

// detectInstallation resolves automatic choices before rendering any installation assets.
func detectInstallation(ctx context.Context, requested Options) (Options, hostDetection, error) {
	o := defaults(requested)
	installed, err := readInstallation(o.StateDir)
	// Unreadable ownership cannot authorize a fresh installation.
	if err != nil {
		return o, hostDetection{}, err
	}
	// Repeated init derives its identity from the installed configuration rather
	// than requiring the operator to re-enter the original domain and hostname.
	if installed != nil {
		if requested.Domain == "" {
			server, err := config.ReadServer(o.ConfigPath)
			if err != nil {
				return o, hostDetection{}, fmt.Errorf("read installed application domain from %s (supply --domain to repair): %w", o.ConfigPath, err)
			}
			requested.Domain = server.Domains[0]
		}
		if requested.ControllerHostname == "" {
			requested.ControllerHostname = installed.ControllerHostname
			if requested.ControllerHostname == "" {
				requested.ControllerHostname = installed.Hostname
			}
		}
	}
	detected := hostDetection{}
	// Local client machines do not supply evidence about the intended Linux installation target.
	if runtime.GOOS == "linux" {
		detected.Docker = detectComponent(ctx, "docker", []string{"docker", "dockerd"}, []string{"docker.io", "docker-ce"}, []string{"/var/run/docker.sock", "/etc/docker/daemon.json", "/var/lib/docker"})
		detected.Nginx = detectComponent(ctx, "nginx", []string{"nginx"}, []string{"nginx", "nginx-core", "nginx-full"}, []string{"/etc/nginx/nginx.conf"})
	}
	resolved := resolveDetectedOptions(requested, installed, detected)
	// Apply ownership policy before touching credentials or rendering service assets.
	if err := validateInstallationRuntime(resolved, installed); err != nil {
		return resolved, detected, err
	}
	// Lost ownership metadata must not turn a used controller into a fresh installation.
	if installed == nil {
		if err := checkFreshInstallationState(resolved.StateDir, "/etc/systemd/system/hostlier.service"); err != nil {
			return resolved, detected, err
		}
	}
	resolved, err = resolveDockerLogin(resolved, installed)
	return resolved, detected, err
}

// validateInstallationRuntime admits K3s on fresh hosts and keeps existing installs on their recorded runtime.
func validateInstallationRuntime(o Options, installed *Installation) error {
	// A prepared or running installation already owns its runtime choice.
	if installed != nil {
		// An incomplete or unsupported record requires recovery before any host changes.
		if installed.Runtime != "docker" && installed.Runtime != "k3s" {
			return model.Err("INSTALL_STATE", "Existing Hostlier installation has no supported recorded runtime; recover its installation record before continuing")
		}
		// Runtime changes go through the separately approved migration workflow.
		if o.Runtime != installed.Runtime {
			return model.Err("INSTALL_RUNTIME", "Existing Hostlier installation uses "+installed.Runtime+"; keep its recorded runtime or use hostlier-controller migrate")
		}
		return nil
	}
	// Fresh installations use the Hostlier-managed K3s cluster.
	if o.Runtime != "k3s" {
		return model.Err("INSTALL_RUNTIME", "New Hostlier installations require k3s; use --runtime k3s or omit --runtime")
	}
	return nil
}

// checkFreshInstallationState refuses fresh ownership when a controller or runtime ledger already exists.
func checkFreshInstallationState(stateDir, serviceUnit string) error {
	entries, err := os.ReadDir(stateDir)
	// An inaccessible state directory cannot establish fresh ownership.
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Pre-provisioned node policy and other configuration can exist before installation.
	for _, entry := range entries {
		name := entry.Name()
		// Persisted controller, runtime, and resource ledgers require the recorded install identity.
		if isInstallationLedger(name) {
			return model.Err("INSTALL_STATE", "Existing Hostlier state has no installation.json; recover its installation record before continuing")
		}
	}
	// The installed unit can identify an older controller whose state path was omitted from the command.
	if _, err := os.Lstat(serviceUnit); !os.IsNotExist(err) {
		return model.Err("INSTALL_STATE", "Existing Hostlier service has no installation record at this state directory; use its original state directory or recover installation.json")
	}
	return nil
}

// isInstallationLedger recognizes persistent ownership independently of the selected backend.
func isInstallationLedger(name string) bool {
	return name == "state.json" || name == "state.db" || strings.HasPrefix(name, "state.db-") ||
		strings.HasSuffix(name, ".json") && (strings.HasPrefix(name, "runtime-") || strings.HasPrefix(name, "resources-"))
}

// readInstallation distinguishes first installation from corrupt or inaccessible durable ownership.
func readInstallation(stateDir string) (*Installation, error) {
	path := filepath.Join(stateDir, "installation.json")
	info, err := os.Lstat(path)
	// Only an absent record permits establishing new ownership.
	if os.IsNotExist(err) {
		return nil, nil
	}
	// Preserve failures instead of silently falling back to fresh installation.
	if err != nil {
		return nil, err
	}
	// A redirected or nonregular record cannot establish server ownership.
	if !info.Mode().IsRegular() {
		return nil, model.Err("INSTALL_STATE", "Existing installation record must be a regular file")
	}
	data, err := os.ReadFile(path)
	// A failed read after checking identity must never be treated as a fresh install.
	if err != nil {
		return nil, err
	}
	var installed Installation
	// Malformed records must be repaired before an automatic choice can be made.
	if json.Unmarshal(data, &installed) != nil || strings.TrimSpace(string(data)) == "null" {
		return nil, model.Err("INSTALL_STATE", "Existing installation state is unreadable")
	}
	// Unknown ownership cannot be interpreted as permission to manage a daemon.
	if installed.DockerMode != "" && installed.DockerMode != "existing" && installed.DockerMode != "managed" {
		return nil, model.Err("INSTALL_STATE", "Unknown Docker ownership in installation state")
	}
	return &installed, nil
}

// detectComponent recognizes stopped services and residual configuration as well as running binaries.
func detectComponent(ctx context.Context, service string, binaries, packages, paths []string) bool {
	// Configuration or storage left by an installation still belongs to the operator.
	for _, path := range paths {
		// Permission errors are also evidence; they must not authorize replacement.
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return true
		}
	}
	// A non-distribution binary is still an existing installation.
	for _, binary := range binaries {
		// Compatibility is verified later against the actual daemon and service.
		if _, err := exec.LookPath(binary); err == nil {
			return true
		}
	}
	// Package evidence covers installations whose executables are outside the current PATH.
	for _, name := range packages {
		// Even an inactive installed package prevents automatic replacement.
		if component(ctx, name).Installed {
			return true
		}
	}
	state, ok := command(ctx, "systemctl", "show", service+".service", "--property=LoadState", "--value")
	return !ok || state != "not-found"
}

// automatic marks omitted choices and the explicit auto spelling identically.
func automatic(value string) bool { return value == "" || value == "auto" }

// resolveDetectedOptions selects runtime and proxy independently and preserves ownership on retries.
func resolveDetectedOptions(requested Options, installed *Installation, detected hostDetection) Options {
	o := defaults(requested)
	if installed != nil {
		if o.MasterFingerprint == "" {
			o.MasterFingerprint = installed.MasterFingerprint
		}
		// An explicit copy of the same pin must not erase its retained endpoint.
		if o.MasterURL == "" && o.MasterFingerprint == installed.MasterFingerprint {
			o.MasterURL = installed.MasterURL
		}
	}
	// Existing Hostlier identity wins over fresh auto-selection; explicit runtime flags remain authoritative.
	if automatic(requested.Runtime) {
		o.Runtime = "k3s"
		// Retain the installed runtime when init is repeated without flags.
		if installed != nil && installed.Runtime != "" {
			o.Runtime = installed.Runtime
		}
	}
	// Proxy detection does not depend on the selected container runtime.
	if automatic(requested.Proxy) {
		o.Proxy = detectedProxy(installed, detected.Nginx)
	}
	o.DockerMode = ""
	// Alternative runtimes leave Docker ownership with the operator.
	if o.Runtime == "docker" {
		o.DockerMode = detectedDockerMode(installed, detected.Docker)
	}
	return o
}

// detectedProxy reuses an existing Nginx installation or keeps the durable Hostlier proxy selection.
func detectedProxy(installed *Installation, nginx bool) string {
	// Legacy records without a proxy name describe the original Caddy installation.
	if installed != nil {
		// Preserve the exact recorded proxy mode instead of rediscovering our own binary as foreign.
		if installed.Proxy != "" {
			return installed.Proxy
		}
		return "caddy"
	}
	// Presence selects coexistence even if compatibility checks will subsequently fail.
	if nginx {
		return "nginx-existing"
	}
	return "caddy"
}

// detectedDockerMode records external daemon ownership independently of the edge choice.
func detectedDockerMode(installed *Installation, docker bool) string {
	// Persisted choices prevent retries from claiming the external daemon or reclassifying our own installation.
	if installed != nil {
		// New records store runtime ownership explicitly.
		if installed.DockerMode != "" {
			return installed.DockerMode
		}
		// The initial coexistence release always reused Docker.
		if installed.Proxy == "nginx-existing" {
			return "existing"
		}
		return "managed"
	}
	// Missing Docker is installed, while any existing evidence requires read-only compatibility checks.
	if docker {
		return "existing"
	}
	return "managed"
}

// externalDocker is the independent boundary for package, service and firewall mutation decisions.
func externalDocker(o Options) bool { return o.Runtime == "docker" && o.DockerMode == "existing" }

// inspectHostInfrastructure validates each selected component through its own ownership boundary.
func inspectHostInfrastructure(ctx context.Context, p *Plan) {
	inspectPackages(ctx, p)
	inspectSystemd(ctx, p)
	inspectInstallationRecord(ctx, p)
	inspectUnownedAssets(ctx, p)
	// Docker compatibility comes from the daemon rather than a distribution package pin when reusing it.
	if externalDocker(p.Options) {
		inspectDockerReuse(ctx, p)
	}
	inspectExternalPackages(ctx, p)
	inspectPackageVersions(ctx, p)
	inspectExternalK3s(ctx, p)
	inspectK3sVersion(ctx, p)
	// The proxy choice is independent of the runtime being reused or installed.
	if p.Proxy == "nginx-existing" {
		inspectSharedInfrastructure(ctx, p)
	} else {
		inspectEdgeOwnership(ctx, p)
	}
	// Existing installations retain operator-managed firewall policy.
	if externalDocker(p.Options) || p.Proxy == "nginx-existing" {
		p.Snapshot.Firewall = "external"
		p.Warnings = append(p.Warnings, "Existing host firewall policy is unchanged; configure any required web access independently")
	} else {
		inspectFirewall(ctx, p)
	}
}

// inspectDockerReuse validates the actual rootful daemon without changing its package or service.
func inspectDockerReuse(ctx context.Context, p *Plan) {
	value, ok := command(ctx, "systemctl", "is-active", "docker.service")
	// A stopped existing daemon is reported for the operator to start, not enabled implicitly.
	if !ok || value != "active" {
		p.Errors = append(p.Errors, "Existing Docker requires an already running docker.service; Hostlier will not start or replace it")
	}
	version, id, err := inspectExistingDocker(ctx)
	// Unsupported Docker does not fall back to installing a replacement package.
	if err != nil {
		p.Errors = append(p.Errors, err.Error())
	}
	p.Snapshot.Docker = Component{Installed: true, Version: version}
	p.Snapshot.ExistingDockerID = id
}

// describeRuntimeChoice explains independent runtime ownership without implying changes to external services.
func describeRuntimeChoice(p *Plan) string {
	// External engines retain all package, service and existing workload ownership.
	if externalDocker(p.Options) {
		return "Reuse existing Docker at /var/run/docker.sock; preserve daemon configuration, service, containers, networks and volumes"
	}
	// Explicit k3s uses its own containerd and leaves any existing Docker daemon alone.
	if p.Runtime == "k3s" {
		return "Use Hostlier-managed k3s with bundled containerd; leave any existing Docker installation untouched"
	}
	return fmt.Sprintf("Install or retain Hostlier-managed Docker %s", dockerPackageVersion(*p))
}
