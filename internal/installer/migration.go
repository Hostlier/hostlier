package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"hostlier/internal/model"
)

// InspectK3sMigration inspects only new cluster prerequisites. Ordinary init
// retains its runtime-change prohibition; migration never invokes init to bypass it.
func InspectK3sMigration(ctx context.Context, stateDir string, reuse bool) []string {
	p := Plan{Options: Options{Runtime: "k3s"}}
	inspectPlatform(&p)
	inspectExternalK3s(ctx, &p)
	// Reject incompatible state before continuing the protected migration step.
	if reuse {
		p.Errors = append(p.Errors, inspectOwnedMigrationAssets(ctx, stateDir)...)
	} else {
		p.Errors = append(p.Errors, inspectFreshMigrationAssets()...)
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if swaps, err := os.ReadFile("/proc/swaps"); err != nil || len(strings.Fields(string(swaps))) > 5 {
		p.Errors = append(p.Errors, "Disable host swap before migration; no-swap workloads require a node without active swap")
	}
	// Reject incompatible state before continuing the protected migration step.
	if out, ok := command(ctx, "tar", "--version"); !ok || !strings.Contains(out, "GNU tar") {
		p.Errors = append(p.Errors, "Migration requires GNU tar for numeric ownership, ACLs, extended attributes and sparse files")
	}
	return p.Errors
}

// inspectFreshMigrationAssets prevents adoption of an unrelated existing cluster.
func inspectFreshMigrationAssets() []string {
	problems := []string{}
	// Complete every selected item before advancing the migration boundary.
	for _, path := range []string{"/usr/local/bin/k3s", "/var/lib/rancher/k3s", "/etc/systemd/system/k3s.service"} {
		// Preserve failure before advancing to the next external-effect boundary.
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			problems = append(problems, "Existing Kubernetes assets cannot be adopted by migration: "+path)
		}
	}
	return problems
}

// inspectOwnedMigrationAssets permits repair of missing, but never changed, owned assets.
func inspectOwnedMigrationAssets(ctx context.Context, stateDir string) []string {
	problems := []string{}
	// Reject incompatible state before continuing the protected migration step.
	if !migrationVersionPinned(ctx) {
		problems = append(problems, "Retry requires the exact previously installed k3s version")
	}
	// Complete every selected item before advancing the migration boundary.
	for path, body := range k3sAssets() {
		expected, err := expectedMigrationAsset(stateDir, path, body)
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		raw, err := os.ReadFile(path)
		// Preserve failure before advancing to the next external-effect boundary.
		if !os.IsNotExist(err) && (err != nil || string(raw) != expected) {
			problems = append(problems, "Previously owned Kubernetes asset changed: "+path)
		}
	}
	return problems
}

// migrationVersionPinned allows a missing binary to be downloaded after failed provisioning.
func migrationVersionPinned(ctx context.Context) bool {
	// Reject incompatible state before continuing the protected migration step.
	if !migrationFileExists("/usr/local/bin/k3s") {
		return true
	}
	version, ok := command(ctx, "/usr/local/bin/k3s", "--version")
	return ok && strings.Contains(version, " "+K3sVersion+" ")
}

// expectedMigrationAsset retains reviewed kubelet limits when checking retry drift.
func expectedMigrationAsset(stateDir, path, body string) (string, error) {
	// Reject incompatible state before continuing the protected migration step.
	if path == "/etc/systemd/system/k3s.service" {
		return k3sMigrationFlags(stateDir, body)
	}
	return body, nil
}

// InstallMigrationK3s installs only the reviewed cluster and its private guards.
// Public proxy, controller configuration, and Docker service ownership are retained.
func InstallMigrationK3s(ctx context.Context, stateDir string, reuse bool, policy []byte) error {
	p, err := migrationClusterPlan(ctx, stateDir, reuse, policy)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	run := migrationCommand(ctx)
	// Preserve failure before advancing to the next external-effect boundary.
	if err := installK3s(ctx, p, run); err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if reuse {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := run("systemctl", "restart", "k3s.service"); err != nil {
			return err
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err := waitK3sReady(ctx); err != nil {
			return err
		}
	}
	return migrationPodFirewall(ctx, run)
}

// migrationClusterPlan validates old assets before publishing the new node policy.
func migrationClusterPlan(ctx context.Context, stateDir string, reuse bool, policy []byte) (Plan, error) {
	p := Plan{Options: Options{Runtime: "k3s", StateDir: stateDir}}
	// Reject incompatible state before continuing the protected migration step.
	if problems := InspectK3sMigration(ctx, stateDir, reuse); len(problems) > 0 {
		return p, fmt.Errorf("Kubernetes migration preflight: %s", strings.Join(problems, "; "))
	}
	// Require the declared confinement contract before accepting this workload.
	if len(policy) > 0 {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := atomic(filepath.Join(stateDir, "k3s-security.json"), policy, 0600); err != nil {
			return p, err
		}
	}
	run := migrationCommand(ctx)
	// Complete every selected item before advancing the migration boundary.
	for _, cmd := range [][]string{{"apt-get", "update"}, {"apt-get", "install", "-y", "--no-remove", "--no-install-recommends", "iptables", "ca-certificates"}} {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := run(cmd[0], cmd[1:]...); err != nil {
			return p, err
		}
	}
	// Reject incompatible state before continuing the protected migration step.
	if reuse && migrationFileExists("/usr/local/bin/k3s") {
		p.Snapshot.K3s = Component{Installed: true, Version: K3sVersion}
	}
	return p, nil
}

// migrationCommand hides command output that may contain protected host details.
func migrationCommand(ctx context.Context) func(string, ...string) error {
	// Keep the callback bound to the current migration and its checkpoint.
	return func(file string, args ...string) error {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := exec.CommandContext(ctx, file, args...).Run(); err != nil {
			return fmt.Errorf("%s failed: %w", file, err)
		}
		return nil
	}
}

// migrationPodFirewall opens only the fixed pod subnet when UFW manages forwarding.
func migrationPodFirewall(ctx context.Context, run func(string, ...string) error) error {
	out, ok := command(ctx, "ufw", "status")
	// Reject incompatible state before continuing the protected migration step.
	if !ok || !strings.Contains(out, "Status: active") {
		return nil
	}
	// Complete every selected item before advancing the migration boundary.
	for _, args := range [][]string{{"allow", "in", "on", "cni0", "from", "10.42.0.0/16", "comment", "hostlier-k3s-pods"}, {"route", "allow", "in", "on", "cni0", "from", "10.42.0.0/16", "comment", "hostlier-k3s-pods"}, {"route", "allow", "out", "on", "cni0", "to", "10.42.0.0/16", "comment", "hostlier-k3s-pods"}} {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := run("ufw", args...); err != nil {
			return err
		}
	}
	return nil
}

// migrationFileExists keeps this migration step within its validated recovery boundary.
func migrationFileExists(path string) bool { _, err := os.Lstat(path); return !os.IsNotExist(err) }

// k3sMigrationFlags also runs on later init/update, so node guarantees survive upgrades.
func k3sMigrationFlags(stateDir, unit string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, "k3s-security.json"))
	// Preserve failure before advancing to the next external-effect boundary.
	if os.IsNotExist(err) {
		return unit, nil
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return "", err
	}
	var policy struct {
		Version      int
		PodPidsLimit int64
	}
	// Require the declared confinement contract before accepting this workload.
	if json.Unmarshal(raw, &policy) != nil || policy.Version != 1 || policy.PodPidsLimit < 16 || policy.PodPidsLimit > 4096 {
		return "", model.Err("K3S_SECURITY", "Invalid managed Kubernetes node policy")
	}
	flags := fmt.Sprintf(" --kubelet-arg=pod-max-pids=%d --kubelet-arg=fail-swap-on=true --kubelet-arg=root-dir=/var/lib/kubelet", policy.PodPidsLimit)
	return strings.Replace(unit, "--secrets-encryption", "--secrets-encryption"+flags, 1), nil
}
