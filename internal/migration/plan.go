// Package migration implements an offline, journaled Docker to k3s handoff.
package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"hostlier/internal/controller"
	"hostlier/internal/installer"
	"hostlier/internal/model"
	runtimes "hostlier/internal/runtime"
	"hostlier/internal/store"
)

// Options retains the typed inputs required by this migration step.
type Options struct {
	StateDir  string `json:"stateDirectory"`
	Config    string `json:"config"`
	BackupDir string `json:"backupDirectory"`
	Retry     bool   `json:"retry,omitempty"`
}

// Plan retains the typed inputs required by this migration step.
type Plan struct {
	Options
	From                 string                      `json:"from"`
	To                   string                      `json:"to"`
	Applications         []string                    `json:"applications"`
	Volumes              []runtimes.MigrationVolume  `json:"volumes"`
	Errors               []string                    `json:"errors"`
	Changes              []string                    `json:"changes"`
	Fingerprint          string                      `json:"fingerprint"`
	SourceDigest         string                      `json:"sourceDigest"`
	Installation         installer.Installation      `json:"installation"`
	Security             *runtimes.K3sSecurityPolicy `json:"security,omitempty"`
	ServiceDigest        string                      `json:"serviceDigest"`
	ConfigDigest         string                      `json:"configDigest"`
	PreviousID           string                      `json:"previousMigration,omitempty"`
	PreviousApplications []string                    `json:"previousApplications,omitempty"`
}

// Inspect is read-only, including while the ordinary controller is running.
func Inspect(ctx context.Context, o Options) (Plan, error) {
	var state controller.State
	// Preserve failure before advancing to the next external-effect boundary.
	if err := validateOptions(o); err != nil {
		return Plan{}, err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := store.Snapshot(o.StateDir, &state); err != nil {
		return Plan{}, err
	}
	return inspectState(ctx, o, state)
}

// inspectState assembles one immutable review from the committed controller and host facts.
func inspectState(ctx context.Context, o Options, state controller.State) (Plan, error) {
	p := newPlan(o, state.Runtime)
	// Preserve failure before advancing to the next external-effect boundary.
	if err := p.loadInputs(); err != nil {
		return p, err
	}
	p.checkController(state)
	p.checkWork(state)
	p.checkApplications(state)
	// Preserve failure before advancing to the next external-effect boundary.
	if err := p.inspectRetry(); err != nil {
		return p, err
	}
	p.Errors = append(p.Errors, installer.InspectK3sMigration(ctx, o.StateDir, p.PreviousID != "")...)
	// Preserve failure before advancing to the next external-effect boundary.
	if err := p.inspectRuntime(ctx, state); err != nil {
		return p, err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := p.bindSource(state); err != nil {
		return p, err
	}
	sort.Strings(p.Errors)
	p.Fingerprint = digestJSON(p)
	return p, nil
}

// newPlan describes the downtime, data handoff and rollback boundary before approval.
func newPlan(o Options, from string) Plan {
	return Plan{Options: o, From: from, To: "k3s", Applications: []string{}, Errors: []string{}, Changes: []string{
		"Stop the controller and fence all owned Docker application containers; planned application downtime",
		"Save and read back a checksummed controller backup and cold archives of every primary and sidecar volume",
		"Install Hostlier's pinned single-node k3s without changing the public proxy or external Docker service",
		"Restore independent retained local Kubernetes volumes, deploy the same immutable images and pinned secrets, and verify readiness and HTTPS",
		"Retain stopped Docker containers, original data and backups for explicit rollback; rollback copies current target data back before starting Docker",
	}}
}

// loadInputs binds systemd and configuration bytes without opening a writer store.
func (p *Plan) loadInputs() error {
	data, err := os.ReadFile(filepath.Join(p.StateDir, "installation.json"))
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := json.Unmarshal(data, &p.Installation); err != nil {
		return err
	}
	unit, err := os.ReadFile(serviceUnit)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	p.ServiceDigest = digestJSON(unit)
	cfg, err := os.ReadFile(p.Config)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	p.ConfigDigest = digestJSON(cfg)
	// Preserve failure before advancing to the next external-effect boundary.
	if _, err := os.Lstat(p.BackupDir); !os.IsNotExist(err) {
		p.Errors = append(p.Errors, "Backup directory must be new and absent")
	}
	return nil
}

// checkController bounds this workflow to standalone application migration.
func (p *Plan) checkController(state controller.State) {
	// Reject incompatible state before continuing the protected migration step.
	if state.Version != 2 || state.Runtime != "docker" || p.Installation.Runtime != "docker" {
		p.Errors = append(p.Errors, "An initialized Docker controller is required")
	}
	// Treat absent optional state explicitly rather than inferring authority.
	if p.Installation.MasterFingerprint != "" || state.MasterFingerprint != nil || len(state.Hosts) > 0 {
		p.Errors = append(p.Errors, "Fleet controllers and agents require a separate host migration; this workflow supports a standalone server")
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(state.Tunnels) > 0 {
		p.Errors = append(p.Errors, "Remove active Bridge tunnels before migration")
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(state.Resources) > 0 {
		p.Errors = append(p.Errors, "Managed database resources must be exported and migrated separately; this workflow supports application and sidecar volumes")
	}
}

// operationTerminal keeps uncertain or accepted work out of the offline handoff.
func operationTerminal(status string) bool {
	// Select only representations explicitly supported by the migration contract.
	switch status {
	// Keep this alternative within the validated operation or archive contract.
	case "succeeded", "failed", "cancelled", "superseded":
		return true
	// Keep this alternative within the validated operation or archive contract.
	default:
		return false
	}
}

// checkWork requires background planning and accepted operations to be settled.
func (p *Plan) checkWork(state controller.State) {
	// Complete every selected item before advancing the migration boundary.
	for _, op := range state.Operations {
		// Treat absent optional state explicitly rather than inferring authority.
		if op != nil && !operationTerminal(op.Status) {
			p.Errors = append(p.Errors, "Resolve unfinished operations before migration")
			break
		}
	}
	// Complete every selected item before advancing the migration boundary.
	for _, job := range state.PlanningJobs {
		// Treat absent optional state explicitly rather than inferring authority.
		if job != nil && (job.Status == "queued" || job.Status == "running") {
			p.Errors = append(p.Errors, "Wait for planning jobs to finish before migration")
			break
		}
	}
}

// checkApplications inventories eligible workloads and the strongest shared node budget.
func (p *Plan) checkApplications(state controller.State) {
	// Apply the identity checks to every application in the server-wide handoff.
	for _, app := range state.Applications {
		p.checkApplication(app)
	}
	sort.Strings(p.Applications)
	p.Security = securityPolicy(state)
	// Require the declared confinement contract before accepting this workload.
	if p.Security != nil {
		p.Changes = append(p.Changes, fmt.Sprintf("Enforce a shared %d-process Pod ceiling, disable kubelet swap, preserve nonroot identity/read-only root/capability drops, and initialize bounded private memory scratch volumes with a pinned helper", p.Security.PodPidsLimit))
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if _, err := applicationOrder(state.Applications); err != nil {
		p.Errors = append(p.Errors, err.Error())
	}
}

// checkApplication rejects lifecycle states that do not have a single known writer.
func (p *Plan) checkApplication(app *controller.Application) {
	// Treat absent optional state explicitly rather than inferring authority.
	if app == nil {
		p.Errors = append(p.Errors, "Invalid application record")
		return
	}
	// Reject incompatible state before continuing the protected migration step.
	if app.Status == "deleted" {
		p.Errors = append(p.Errors, "Separately recover or purge deleted application records and data before migration")
		return
	}
	p.Applications = append(p.Applications, app.Name)
	// Reject incompatible state before continuing the protected migration step.
	if app.Status != "running" || app.Spec.SleepAfter != "" || model.DesiredReplicas(app.Spec) != 1 {
		p.Errors = append(p.Errors, app.Name+": requires running single-replica capacity with idle scaling disabled")
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(app.ResourceRefs) > 0 || len(app.Spec.Dependencies) > 0 {
		p.Errors = append(p.Errors, app.Name+": managed database dependencies require a separate migration")
	}
}

// inspectRetry authorizes reuse only through a fully resolved previous journal.
func (p *Plan) inspectRetry() error {
	// Reuse prior recovery state only through the reviewed retry path.
	if p.Retry {
		prior, err := Read(p.StateDir)
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		// Only a resolved phase can release the migration startup guard.
		if prior.Phase != "rolled-back" {
			p.Errors = append(p.Errors, "Retry requires a fully rolled-back previous migration")
			return nil
		}
		p.PreviousID = prior.ID
		p.PreviousApplications = prior.Plan.Applications
		return nil
	}
	// Complete every selected item before advancing the migration boundary.
	for _, name := range []string{"runtime-k3s.json", "migration.json"} {
		// Preserve failure before advancing to the next external-effect boundary.
		if _, err := os.Lstat(filepath.Join(p.StateDir, name)); !os.IsNotExist(err) {
			p.Errors = append(p.Errors, "Existing "+name+" requires status/rollback or explicit archival before another migration")
		}
	}
	return nil
}

// inspectRuntime verifies source ownership and prepares target manifests without mutation.
func (p *Plan) inspectRuntime(ctx context.Context, state controller.State) error {
	docker, kube, err := adapters(p.Options, p.Installation, p.Security)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	inv, err := docker.MigrationInventory(ctx)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		p.Errors = append(p.Errors, err.Error())
	} else {
		p.checkInventory(state, inv)
		p.checkSpace(ctx)
	}
	// Apply the identity checks to every application in the server-wide handoff.
	for _, name := range p.Applications {
		app := state.Applications[name]
		var preview model.Project
		raw, _ := json.Marshal(app.Spec)
		json.Unmarshal(raw, &preview)
		removeApplicationBindings(&preview)
		// Preserve failure before advancing to the next external-effect boundary.
		if _, err := kube.K3sManifests(preview, model.DeployContext{OperationID: "migration-preview", Secrets: placeholderSecrets(app)}); err != nil {
			p.Errors = append(p.Errors, name+": "+err.Error())
		}
	}
	return nil
}

// checkInventory requires both durable metadata layers to identify the same deployments.
func (p *Plan) checkInventory(state controller.State, inv runtimes.MigrationInventory) {
	p.Volumes = inv.Volumes
	// Reject incompatible state before continuing the protected migration step.
	if len(inv.Applications) != len(p.Applications) {
		p.Errors = append(p.Errors, "Controller and Docker application inventories disagree")
	}
	// Apply the identity checks to every application in the server-wide handoff.
	for _, name := range p.Applications {
		app := state.Applications[name]
		actual := inv.Applications[name]
		// Reject incompatible state before continuing the protected migration step.
		if actual.RuntimeID != app.RuntimeID || actual.ImageDigest != app.ImageDigest {
			p.Errors = append(p.Errors, name+": controller and Docker deployment identities disagree")
		}
	}
}

// checkSpace uses the actual volume footprint instead of unenforced Docker size declarations.
func (p *Plan) checkSpace(ctx context.Context) {
	// Reject incompatible state before continuing the protected migration step.
	if runtime.GOOS != "linux" {
		return
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := spacePreflight(ctx, p); err != nil {
		p.Errors = append(p.Errors, err.Error())
	}
}

// bindSource invalidates approval when desired state or source runtime ownership changes.
func (p *Plan) bindSource(state controller.State) error {
	raw, err := os.ReadFile(filepath.Join(p.StateDir, "runtime-docker.json"))
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	p.SourceDigest = digestJSON(struct {
		Generation   int64
		Applications map[string]*controller.Application
		Installation installer.Installation
		Runtime      json.RawMessage
	}{state.Generation, state.Applications, p.Installation, raw})
	return nil
}

// placeholderSecrets keeps this migration step within its validated recovery boundary.
func placeholderSecrets(app *controller.Application) map[string]string {
	out := map[string]string{}
	// Complete every selected item before advancing the migration boundary.
	for key := range app.SecretRefs {
		out[key] = "migration-preview"
	}
	return out
}

// adapters keeps this migration step within its validated recovery boundary.
func adapters(o Options, installed installer.Installation, security *runtimes.K3sSecurityPolicy) (*runtimes.Runtime, *runtimes.K3s, error) {
	options := runtimes.Options{Type: "docker", StateDir: o.StateDir, Hostname: installed.ControllerHostname, Proxy: installed.Proxy, ControllerUpstream: "127.0.0.1:7463", DockerLogin: installed.DockerLogin, K3sSecurity: security}
	d, err := runtimes.New(options)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, nil, err
	}
	options.Type = "k3s"
	k, err := runtimes.New(options)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, nil, err
	}
	return d.(*runtimes.Runtime), k.(*runtimes.K3s), nil
}

// securityPolicy keeps this migration step within its validated recovery boundary.
func securityPolicy(state controller.State) *runtimes.K3sSecurityPolicy {
	var policy *runtimes.K3sSecurityPolicy
	// Keep the callback bound to the current migration and its checkpoint.
	consider := func(p model.Project) {
		// Require the declared confinement contract before accepting this workload.
		if p.Security == nil {
			return
		}
		// Require the declared confinement contract before accepting this workload.
		if policy == nil {
			policy = &runtimes.K3sSecurityPolicy{Version: 1, PodPidsLimit: p.Security.PidsLimit}
			// Require the declared confinement contract before accepting this workload.
		} else if p.Security.PidsLimit < policy.PodPidsLimit {
			policy.PodPidsLimit = p.Security.PidsLimit
		}
	}
	// Apply the identity checks to every application in the server-wide handoff.
	for _, app := range state.Applications {
		// Treat absent optional state explicitly rather than inferring authority.
		if app == nil {
			continue
		}
		consider(app.Spec)
		// Complete every selected item before advancing the migration boundary.
		for _, m := range app.Spec.Sidecars {
			consider(m)
		}
	}
	return policy
}

// removeApplicationBindings keeps this migration step within its validated recovery boundary.
func removeApplicationBindings(p *model.Project) {
	// Complete every selected item before advancing the migration boundary.
	for key, ref := range p.Bindings {
		// Reject incompatible state before continuing the protected migration step.
		if strings.HasPrefix(ref, "applications.") {
			delete(p.Bindings, key)
		}
	}
	// Complete every selected item before advancing the migration boundary.
	for name, member := range p.Sidecars {
		removeApplicationBindings(&member)
		p.Sidecars[name] = member
	}
}

// digestJSON keeps this migration step within its validated recovery boundary.
func digestJSON(v any) string {
	data, _ := json.Marshal(v)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// validateOptions keeps this migration step within its validated recovery boundary.
func validateOptions(o Options) error {
	// Complete every selected item before advancing the migration boundary.
	for _, p := range []string{o.StateDir, o.Config, o.BackupDir} {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := validateMigrationPath(p); err != nil {
			return err
		}
	}
	rel, err := filepath.Rel(o.StateDir, o.BackupDir)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("backup directory must be outside controller state")
	}
	return nil
}

// validateMigrationPath keeps filesystem and systemd path interpretation identical.
func validateMigrationPath(p string) error {
	// Reject incompatible state before continuing the protected migration step.
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.ContainsAny(p, " \t\r\n%\\\"") {
		return fmt.Errorf("migration requires clean absolute paths without whitespace or systemd substitutions")
	}
	return safeAncestors(filepath.Dir(p))
}
