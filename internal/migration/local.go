package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hostlier/internal/installer"
	"hostlier/internal/model"
	runtimes "hostlier/internal/runtime"
	"hostlier/internal/store"
)

const serviceUnit = "/etc/systemd/system/hostlier.service"

// localHost retains the typed inputs required by this migration step.
type localHost struct {
	options   Options
	installed installer.Installation
	docker    *runtimes.Runtime
	kube      *runtimes.K3s
}

// localEngine keeps this migration step within its validated recovery boundary.
func localEngine(o Options, installed installer.Installation) (*engine, error) {
	d, k, err := adapters(o, installed, nil)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	h := &localHost{options: o, installed: installed, docker: d, kube: k}
	// Keep the callback bound to the current migration and its checkpoint.
	return &engine{host: h, backup: createArchive, restore: restoreArchive, inspect: inspectState, export: func(s *store.Store, output, config string) error {
		return s.Export(output, config, "/etc/hostlier", "/var/lib/caddy")
	}}, nil
}

// localCommand keeps this migration step within its validated recovery boundary.
func localCommand(ctx context.Context, file string, args ...string) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := exec.CommandContext(ctx, file, args...).Run(); err != nil {
		return fmt.Errorf("%s failed; inspect protected server diagnostics: %w", file, err)
	}
	return nil
}

// service keeps this migration step within its validated recovery boundary.
func (h *localHost) service(ctx context.Context, action string) error {
	return localCommand(ctx, "systemctl", action, "hostlier.service")
}

// unit keeps this migration step within its validated recovery boundary.
func (h *localHost) unit() ([]byte, error) {
	raw, err := os.ReadFile(serviceUnit)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	// Reject incompatible state before continuing the protected migration step.
	if strings.Count(string(raw), "--runtime docker") != 1 || !strings.Contains(string(raw), "--state-dir "+h.options.StateDir+" ") {
		return nil, fmt.Errorf("migration requires the recorded managed Docker service unit")
	}
	return raw, nil
}

// guard keeps this migration step within its validated recovery boundary.
func (h *localHost) guard(active bool) error {
	marker := filepath.Join(h.options.StateDir, "migration-active")
	// Reject incompatible state before continuing the protected migration step.
	if !active {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncDir(h.options.StateDir)
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := writeFile(marker, []byte("Migration in progress; use hostlier-controller migrate status or rollback.\n"), 0600); err != nil {
		return err
	}
	// The systemd condition protects even an older installed executable and survives reboot.
	dir := "/etc/systemd/system/hostlier.service.d"
	// Preserve failure before advancing to the next external-effect boundary.
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	dropin := []byte("[Unit]\nConditionPathExists=!" + marker + "\n")
	path := filepath.Join(dir, "90-hostlier-migration.conf")
	// Preserve failure before advancing to the next external-effect boundary.
	if raw, err := os.ReadFile(path); err == nil && string(raw) != string(dropin) {
		return fmt.Errorf("existing migration service condition belongs to a different state directory")
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := writeFile(path, dropin, 0644); err != nil {
		return err
	}
	return localCommand(context.Background(), "systemctl", "daemon-reload")
}

// install keeps this migration step within its validated recovery boundary.
func (h *localHost) install(ctx context.Context, j *Journal) error {
	reuse := j.Plan.PreviousID != ""

	var policy []byte
	// Require the declared confinement contract before accepting this workload.
	if j.Plan.Security != nil {
		policy, _ = json.Marshal(j.Plan.Security)
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := installer.InstallMigrationK3s(ctx, j.Plan.StateDir, reuse, policy); err != nil {
		return err
	}
	_, k, err := adapters(h.options, h.installed, j.Plan.Security)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	h.kube = k
	// Preserve failure before advancing to the next external-effect boundary.
	if _, err := h.readyNode(ctx); err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if reuse {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := k.MigrationRetireTarget(ctx, j.Plan.PreviousApplications); err != nil {
			return err
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err := archivePreviousRuntime(j); err != nil {
			return err
		}
	}
	return k.MigrationTargetEmpty(ctx, j.Plan.Applications)
}

// archivePreviousRuntime retains prior adapter state before a reviewed retry.
func archivePreviousRuntime(j *Journal) error {
	source := filepath.Join(j.Plan.StateDir, "runtime-k3s.json")
	// Preserve failure before advancing to the next external-effect boundary.
	if _, err := os.Lstat(source); os.IsNotExist(err) {
		return nil
		// Preserve failure before advancing to the next external-effect boundary.
	} else if err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := os.Rename(source, filepath.Join(j.Plan.BackupDir, "previous-runtime-k3s.json")); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := syncDir(j.Plan.StateDir); err != nil {
		return err
	}
	return syncDir(j.Plan.BackupDir)
}

// sourceFence keeps this migration step within its validated recovery boundary.
func (h *localHost) sourceFence(ctx context.Context, j *Journal, checkpoint func() error) error {
	inv, err := h.docker.MigrationInventory(ctx)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(inv.Applications) != len(j.Source) {
		return fmt.Errorf("Docker source inventory changed")
	}
	// Complete every selected item before advancing the migration boundary.
	for name, original := range j.Source {
		// Reject incompatible state before continuing the protected migration step.
		if inv.Applications[name].RuntimeID != original.RuntimeID {
			return fmt.Errorf("Docker source identity changed: %s", name)
		}
	}
	// Reject incompatible state before continuing the protected migration step.
	if digestJSON(inv.Volumes) != digestJSON(j.Plan.Volumes) {
		return fmt.Errorf("Docker volume ownership or mount paths changed")
	}
	return h.docker.MigrationFenceDocker(ctx, j.Policies, checkpoint)
}

// targetPath keeps this migration step within its validated recovery boundary.
func (h *localHost) targetPath(j *Journal, v runtimes.MigrationVolume) string {
	return filepath.Join(j.Plan.StateDir, "migration-data", j.ID, digestJSON(v.Name)[:16])
}

// prepareVolumes keeps this migration step within its validated recovery boundary.
func (h *localHost) prepareVolumes(ctx context.Context, j *Journal) error {
	node, err := h.readyNode(ctx)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Process every primary and sidecar volume within the same writer fence.
	for _, v := range j.Plan.Volumes {
		a, ok := j.Backups[v.Name]
		// Reject incompatible state before continuing the protected migration step.
		if !ok {
			return fmt.Errorf("verified source backup missing: %s", v.Name)
		}
		target := h.targetPath(j, v)
		// Preserve failure before advancing to the next external-effect boundary.
		if err := os.MkdirAll(target, 0700); err != nil {
			return err
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err := restoreArchive(ctx, a, target, false); err != nil {
			return err
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err := h.kube.MigrationPrepareVolume(ctx, v, target, node, j.ID); err != nil {
			return err
		}
	}
	return nil
}

// readyNode keeps this migration step within its validated recovery boundary.
func (h *localHost) readyNode(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	// Bound repeated observations by completion, input exhaustion or cancellation.
	for ctx.Err() == nil {
		raw, err := h.kubectl(ctx, "get", "nodes", "-o", "json")
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return "", err
		}
		node, err := readyNodeName(raw)
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return "", err
		}
		// Reject incompatible state before continuing the protected migration step.
		if node != "" {
			return node, nil
		}
		// Cancellation bounds readiness waiting independently of the next observation.
		select {
		// Cancellation bounds readiness waiting independently of the next observation.
		case <-ctx.Done():
			return "", ctx.Err()
		// Cancellation bounds readiness waiting independently of the next observation.
		case <-time.After(time.Second):
		}
	}
	return "", fmt.Errorf("Kubernetes node did not become ready")
}

// migrationNode carries the label and readiness evidence needed by local PV placement.
type migrationNode struct {
	Metadata struct{ Labels map[string]string }
	Status   struct {
		Conditions []struct{ Type, Status string }
	}
}

// readyNodeName distinguishes an unregistered node from an unsupported multi-node cluster.
func readyNodeName(raw []byte) (string, error) {
	var nodes struct{ Items []migrationNode }
	// Validate the decoded native response before using its contents.
	if json.Unmarshal(raw, &nodes) != nil || len(nodes.Items) > 1 {
		return "", fmt.Errorf("migration requires exactly one Kubernetes node")
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(nodes.Items) == 0 {
		return "", nil
	}
	node := nodes.Items[0]
	name := node.Metadata.Labels["kubernetes.io/hostname"]
	// Reject incompatible state before continuing the protected migration step.
	if name == "" {
		return "", fmt.Errorf("Kubernetes node hostname label is missing")
	}
	// Complete every selected item before advancing the migration boundary.
	for _, condition := range node.Status.Conditions {
		// Reject incompatible state before continuing the protected migration step.
		if condition.Type == "Ready" && condition.Status == "True" {
			return name, nil
		}
	}
	return "", nil
}

// deploy keeps this migration step within its validated recovery boundary.
func (h *localHost) deploy(ctx context.Context, p model.Project, dc model.DeployContext) (model.Deployment, error) {
	return h.kube.Deploy(ctx, p, dc)
}

// restoreSource keeps this migration step within its validated recovery boundary.
func (h *localHost) restoreSource(ctx context.Context, j *Journal) error {
	return h.docker.MigrationRestoreDocker(ctx, j.Policies)
}

// targetFence keeps this migration step within its validated recovery boundary.
func (h *localHost) targetFence(ctx context.Context, j *Journal) error {
	return h.kube.MigrationFenceK3s(ctx, append(append([]string{}, j.Plan.Applications...), j.Plan.PreviousApplications...))
}

// targetUnit keeps this migration step within its validated recovery boundary.
func targetUnit(original []byte) []byte {
	unit := strings.Replace(string(original), "--runtime docker", "--runtime k3s", 1)
	unit = strings.ReplaceAll(unit, "docker.service", "k3s.service")
	return []byte(unit)
}

// setRuntime keeps this migration step within its validated recovery boundary.
func (h *localHost) setRuntime(ctx context.Context, j *Journal, backend string) error {
	current, err := os.ReadFile(serviceUnit)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	target := targetUnit(j.OriginalUnit)
	// Reject incompatible state before continuing the protected migration step.
	if string(current) != string(j.OriginalUnit) && string(current) != string(target) {
		return fmt.Errorf("controller service changed since migration; preserve and review it before switching runtime")
	}
	installed := j.Plan.Installation
	unit := j.OriginalUnit
	// Reject incompatible state before continuing the protected migration step.
	if backend == "k3s" {
		installed.Runtime = "k3s"
		installed.Completed = append(installed.Completed, "k3s")
		unit = target
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := writeJSON(filepath.Join(j.Plan.StateDir, "installation.json"), installed); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := writeFile(serviceUnit, unit, 0644); err != nil {
		return err
	}
	return localCommand(ctx, "systemctl", "daemon-reload")
}

// kubectl keeps this migration step within its validated recovery boundary.
func (h *localHost) kubectl(ctx context.Context, args ...string) ([]byte, error) {
	fixed := append([]string{"kubectl", "--kubeconfig=/etc/rancher/k3s/k3s.yaml", "--context=default", "--request-timeout=20s"}, args...)
	cmd := exec.CommandContext(ctx, "/usr/local/bin/k3s", fixed...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "KUBECTL_KUBERC=false"}
	raw, err := cmd.Output()
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil || len(raw) > 2<<20 {
		return nil, fmt.Errorf("cannot inspect local Kubernetes node")
	}
	return raw, nil
}
