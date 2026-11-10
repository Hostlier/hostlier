package migration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hostlier/internal/controller"
	"hostlier/internal/model"
	runtimes "hostlier/internal/runtime"
	"hostlier/internal/store"
)

// Journal is committed before each external-effect boundary. An unresolved
// journal blocks daemon startup, including after a machine reboot.
type Journal struct {
	Version        int                         `json:"version"`
	ID             string                      `json:"id"`
	Phase          string                      `json:"phase"`
	Plan           Plan                        `json:"plan"`
	Metadata       Archive                     `json:"metadata"`
	Backups        map[string]Archive          `json:"backups"`
	TargetBackups  map[string]Archive          `json:"targetBackups"`
	Policies       map[string]json.RawMessage  `json:"restartPolicies"`
	Source         map[string]model.Deployment `json:"sourceDeployments"`
	Target         map[string]model.Deployment `json:"targetDeployments"`
	OriginalUnit   []byte                      `json:"originalUnit"`
	TargetTouched  bool                        `json:"targetTouched"`
	PreserveTarget bool                        `json:"preserveTarget"`
	RollbackCopied bool                        `json:"rollbackCopied"`
	Specifications map[string]string           `json:"specifications"`
}

// host retains the typed inputs required by this migration step.
type host interface {
	service(context.Context, string) error
	guard(bool) error
	unit() ([]byte, error)
	install(context.Context, *Journal) error
	sourceFence(context.Context, *Journal, func() error) error
	targetFence(context.Context, *Journal) error
	prepareVolumes(context.Context, *Journal) error
	deploy(context.Context, model.Project, model.DeployContext) (model.Deployment, error)
	restoreSource(context.Context, *Journal) error
	setRuntime(context.Context, *Journal, string) error
	targetPath(*Journal, runtimes.MigrationVolume) string
}

// engine retains the typed inputs required by this migration step.
type engine struct {
	host     host
	backup   func(context.Context, string, string) (Archive, error)
	restore  func(context.Context, Archive, string, bool) error
	export   func(*store.Store, string, string) error
	progress func(string)
	inspect  func(context.Context, Options, controller.State) (Plan, error)
}

// Apply rechecks the exact reviewed plan before stopping the controller. On a
// failure it uses a fresh rollback deadline; uncertain fencing keeps both sides
// stopped and the startup guard intact.
func Apply(ctx context.Context, p Plan, approved bool, progress func(string)) error {
	// Reject incompatible state before continuing the protected migration step.
	if !approved {
		return fmt.Errorf("migration requires explicit approval")
	}
	e, err := localEngine(p.Options, p.Installation)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	e.progress = progress
	fresh, err := Inspect(ctx, p.Options)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(p.Errors) > 0 || len(fresh.Errors) > 0 {
		return fmt.Errorf("migration preflight has blockers")
	}
	// Reject incompatible state before continuing the protected migration step.
	if fresh.Fingerprint != p.Fingerprint {
		return fmt.Errorf("migration plan changed; preview and approve again")
	}
	return e.apply(ctx, p)
}

// report keeps this migration step within its validated recovery boundary.
func (e *engine) report(phase string) {
	// Treat absent optional state explicitly rather than inferring authority.
	if e.progress != nil {
		e.progress(phase)
	}
}

// save keeps this migration step within its validated recovery boundary.
func (e *engine) save(j *Journal) error {
	return writeJSON(filepath.Join(j.Plan.StateDir, "migration.json"), j)
}

// phase keeps this migration step within its validated recovery boundary.
func (e *engine) phase(j *Journal, phase string) error {
	j.Phase = phase
	e.report(phase)
	return e.save(j)
}

// apply owns the offline transaction until cutover or verified rollback.
func (e *engine) apply(ctx context.Context, p Plan) (result error) {
	st, state, j, err := e.prepareApply(ctx, p)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	defer st.Close()
	defer e.restoreOnFailure(j, st, state, &result)
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.backupSource(ctx, j, st); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.buildTarget(ctx, j, st, state); err != nil {
		return err
	}
	return e.commitTarget(ctx, j, st, state)
}

// prepareApply acquires the same writer lock used by the running controller.
func (e *engine) prepareApply(ctx context.Context, p Plan) (*store.Store, *controller.State, *Journal, error) {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.service(ctx, "stop"); err != nil {
		return nil, nil, nil, err
	}
	state := &controller.State{}
	st, err := store.Open(p.StateDir, state)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, nil, nil, fmt.Errorf("controller is stopped; cannot acquire exclusive state ownership: %w", err)
	}
	fresh, err := e.inspect(ctx, p.Options, *state)
	// Advance only after the preceding recovery check succeeds.
	if err == nil && fresh.Fingerprint != p.Fingerprint {
		err = fmt.Errorf("source changed during shutdown; controller remains stopped; preview again")
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		st.Close()
		return nil, nil, nil, err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := prepareBackupDirectory(p); err != nil {
		st.Close()
		return nil, nil, nil, err
	}
	j, err := e.prepareJournal(p, *state)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		st.Close()
		return nil, nil, nil, err
	}
	return st, state, j, nil
}

// prepareBackupDirectory preserves a previous rollback journal before replacing it.
func prepareBackupDirectory(p Plan) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := os.Mkdir(p.BackupDir, 0700); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := syncDir(filepath.Dir(p.BackupDir)); err != nil {
		return err
	}
	// Reuse prior recovery state only through the reviewed retry path.
	if p.PreviousID == "" {
		return nil
	}
	prior, err := Read(p.StateDir)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reuse prior recovery state only through the reviewed retry path.
	if prior.ID != p.PreviousID || prior.Phase != "rolled-back" {
		return fmt.Errorf("previous migration changed; review retry again")
	}
	return writeJSON(filepath.Join(p.BackupDir, "previous-migration.json"), prior)
}

// prepareJournal durably binds source identities before fencing any writer.
func (e *engine) prepareJournal(p Plan, state controller.State) (*Journal, error) {
	unit, err := e.host.unit()
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	random := make([]byte, 16)
	// Preserve failure before advancing to the next external-effect boundary.
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	j := &Journal{Version: 1, ID: hex.EncodeToString(random), Plan: p, Phase: "prepared", Backups: map[string]Archive{}, TargetBackups: map[string]Archive{}, Policies: map[string]json.RawMessage{}, Source: map[string]model.Deployment{}, Target: map[string]model.Deployment{}, OriginalUnit: unit, Specifications: map[string]string{}}
	// Apply the identity checks to every application in the server-wide handoff.
	for name, app := range state.Applications {
		j.Source[name] = app.Deployment
		j.Specifications[name] = applicationSignature(app)
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.save(j); err != nil {
		return nil, err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.guard(true); err != nil {
		return nil, err
	}
	return j, nil
}

// restoreOnFailure has an independent deadline even when migration was cancelled.
func (e *engine) restoreOnFailure(j *Journal, st *store.Store, state *controller.State, result *error) {
	// Only a resolved phase can release the migration startup guard.
	if *result == nil || j.Phase == "completed" {
		return
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.rollback(cleanup, j, st, state); err != nil {
		*result = errors.Join(*result, fmt.Errorf("automatic rollback incomplete; both runtimes remain guarded; run migrate rollback: %w", err))
		return
	}
	st.Close()
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.service(cleanup, "start"); err != nil {
		*result = errors.Join(*result, err)
	}
}

// backupSource establishes the metadata and cold-volume recovery boundary.
func (e *engine) backupSource(ctx context.Context, j *Journal, st *store.Store) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.phase(j, "backing-up"); err != nil {
		return err
	}
	file := filepath.Join(j.Plan.BackupDir, "controller.tar")
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.export(st, file, j.Plan.Config); err != nil {
		return err
	}
	// The backup must pass the real restore format before source writers are fenced.
	if err := store.VerifyExport(file); err != nil {
		return err
	}
	var err error
	j.Metadata, err = hashFile(file)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := verifyArchive(j.Metadata); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.save(j); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.sourceFence(ctx, j, func() error { return e.save(j) }); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.backupSourceVolumes(ctx, j); err != nil {
		return err
	}
	return e.phase(j, "backed-up")
}

// backupSourceVolumes checkpoints every verified archive separately.
func (e *engine) backupSourceVolumes(ctx context.Context, j *Journal) error {
	// Process every primary and sidecar volume within the same writer fence.
	for _, v := range j.Plan.Volumes {
		file, err := newArchivePath(j.Plan.BackupDir, "docker-"+digestJSON(v.Name)[:16])
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		a, err := e.backup(ctx, v.Path, file)
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		j.Backups[v.Name] = a
		// Preserve failure before advancing to the next external-effect boundary.
		if err := e.save(j); err != nil {
			return err
		}
	}
	return nil
}

// buildTarget keeps source writers fenced through restore and target readiness.
func (e *engine) buildTarget(ctx context.Context, j *Journal, st *store.Store, state *controller.State) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.install(ctx, j); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.phase(j, "restoring"); err != nil {
		return err
	}
	j.TargetTouched = true
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.save(j); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.prepareVolumes(ctx, j); err != nil {
		return err
	}
	j.PreserveTarget = true
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.save(j); err != nil {
		return err
	}
	return e.deployApplications(ctx, j, st, state)
}

// deployApplications starts private providers before their consumers.
func (e *engine) deployApplications(ctx context.Context, j *Journal, st *store.Store, state *controller.State) error {
	order, err := applicationOrder(state.Applications)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Complete every selected item before advancing the migration boundary.
	for _, name := range order {
		app := state.Applications[name]
		values, err := pinnedSecrets(st, *state, app)
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		deployment, err := e.host.deploy(ctx, app.Spec, model.DeployContext{OperationID: "migration-" + j.ID + "-" + name, Secrets: values})
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		j.Target[name] = deployment
		// Preserve failure before advancing to the next external-effect boundary.
		if err := e.save(j); err != nil {
			return err
		}
	}
	return nil
}

// commitTarget changes metadata only after every application has verified readiness.
func (e *engine) commitTarget(ctx context.Context, j *Journal, st *store.Store, state *controller.State) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.phase(j, "committing"); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.setRuntime(ctx, j, "k3s"); err != nil {
		return err
	}
	// Complete every selected item before advancing the migration boundary.
	for name, deployment := range j.Target {
		state.Applications[name].Deployment = deployment
	}
	state.Runtime = "k3s"
	state.Generation++
	// Preserve failure before advancing to the next external-effect boundary.
	if err := st.Save(state); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.phase(j, "completed"); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.guard(false); err != nil {
		return err
	}
	st.Close()
	// Preserve failure before advancing to the next external-effect boundary.
	if err := e.host.service(ctx, "start"); err != nil {
		return fmt.Errorf("migration completed; controller restart failed; start hostlier.service or run migrate rollback: %w", err)
	}
	return nil
}

// applicationSignature keeps this migration step within its validated recovery boundary.
func applicationSignature(app *controller.Application) string {
	return digestJSON(struct {
		Spec     model.Project
		Secrets  map[string]controller.SecretRef
		Revision string
	}{app.Spec, app.SecretRefs, app.Revision})
}

// pinnedSecrets keeps this migration step within its validated recovery boundary.
func pinnedSecrets(st *store.Store, state controller.State, app *controller.Application) (map[string]string, error) {
	values := map[string]string{}
	// Complete every selected item before advancing the migration boundary.
	for key, ref := range app.SecretRefs {
		secret := state.Secrets[ref.Name]
		// Treat absent optional state explicitly rather than inferring authority.
		if secret == nil || secret.Revisions[ref.Revision] == "" {
			return nil, fmt.Errorf("%s: pinned secret revision is missing", app.Name)
		}
		value, err := st.Decrypt(secret.Revisions[ref.Revision])
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return nil, fmt.Errorf("%s: pinned secret authentication failed", app.Name)
		}
		values[key] = value
	}
	return values, nil
}

// applicationOrder honors private-provider dependencies and rejects cycles.
func applicationOrder(apps map[string]*controller.Application) ([]string, error) {
	graph := workloadOrder{apps: apps, visited: map[string]int{}}
	names := []string{}
	// Complete every selected item before advancing the migration boundary.
	for name := range apps {
		names = append(names, name)
	}
	sort.Strings(names)
	// Complete every selected item before advancing the migration boundary.
	for _, name := range names {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := graph.visit(name); err != nil {
			return nil, err
		}
	}
	return graph.order, nil
}

// workloadOrder tracks dependency traversal without mutating desired application state.
type workloadOrder struct {
	apps    map[string]*controller.Application
	visited map[string]int
	order   []string
}

// visit detects cycles before any dependent workload can start.
func (g *workloadOrder) visit(name string) error {
	// Reject incompatible state before continuing the protected migration step.
	if g.visited[name] == 2 {
		return nil
	}
	// Reject incompatible state before continuing the protected migration step.
	if g.visited[name] == 1 {
		return fmt.Errorf("cyclic private workload bindings require a staged migration")
	}
	app := g.apps[name]
	// Treat absent optional state explicitly rather than inferring authority.
	if app == nil {
		return fmt.Errorf("private provider is missing: %s", name)
	}
	g.visited[name] = 1
	// Complete every selected item before advancing the migration boundary.
	for _, provider := range privateProviders(app.Spec) {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := g.visit(provider); err != nil {
			return err
		}
	}
	g.visited[name] = 2
	g.order = append(g.order, name)
	return nil
}

// privateProviders returns stable dependency identities from scoped environment bindings.
func privateProviders(p model.Project) []string {
	providers := []string{}
	// Complete every selected item before advancing the migration boundary.
	for _, ref := range model.EnvironmentBindings(p) {
		parts := strings.Split(ref, ".")
		// Reject incompatible state before continuing the protected migration step.
		if len(parts) == 3 && parts[0] == "applications" {
			providers = append(providers, parts[1])
		}
	}
	sort.Strings(providers)
	return providers
}
