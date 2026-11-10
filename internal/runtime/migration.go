package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hostlier/internal/model"
)

// MigrationVolume describes a verified, local Docker volume and its target claim.
type MigrationVolume struct {
	Application string `json:"application"`
	Name        string `json:"name"`
	Claim       string `json:"claim"`
	Size        string `json:"size"`
	Path        string `json:"path"`
}

// MigrationInventory contains identities, never container environments or secrets.
type MigrationInventory struct {
	Applications map[string]model.Deployment `json:"applications"`
	Volumes      []MigrationVolume           `json:"volumes"`
}

// MigrationInventory checks every tracked source and refuses interrupted transitions.
func (r *Runtime) MigrationInventory(ctx context.Context) (MigrationInventory, error) {
	state, err := r.read()
	out := MigrationInventory{Applications: map[string]model.Deployment{}}
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return out, err
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(state.Retained) != 0 {
		return out, fmt.Errorf("purge or separately recover retained deleted application data before migration")
	}
	// Apply the identity checks to every application in the server-wide handoff.
	for _, name := range sortedMigrationApps(state) {
		app := state.Apps[name]
		volumes, err := r.inspectMigrationApp(ctx, app)
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return out, err
		}
		out.Applications[name] = app.Deployment
		out.Volumes = append(out.Volumes, volumes...)
	}
	// Keep the callback bound to the current migration and its checkpoint.
	sort.Slice(out.Volumes, func(i, j int) bool { return out.Volumes[i].Name < out.Volumes[j].Name })
	return out, r.migrationVolumeOwnership(ctx, out.Volumes)
}

// validMigrationSource requires a single committed writer with no asynchronous transitions.
func validMigrationSource(app appRecord) bool {
	return app.Status == "running" && !app.IdleSleeping && app.PreviousRuntimeID == "" && app.Autoscaling == nil && model.DesiredReplicas(app.Spec) == 1
}

// migrationVolumeSpec retains the existing Docker and Kubernetes naming contracts.
type migrationVolumeSpec struct {
	key, claim string
	volume     model.Volume
}

// migrationVolumeSpecs includes independently owned primary and sidecar data.
func migrationVolumeSpecs(p model.Project) []migrationVolumeSpec {
	out := []migrationVolumeSpec{}
	// Process every primary and sidecar volume within the same writer fence.
	for key, v := range p.Volumes {
		out = append(out, migrationVolumeSpec{key, "data-" + key, v})
	}
	// Complete every selected item before advancing the migration boundary.
	for member, spec := range p.Sidecars {
		// Process every primary and sidecar volume within the same writer fence.
		for key, v := range spec.Volumes {
			name := "sidecar-" + member + "-" + key
			out = append(out, migrationVolumeSpec{name, name, v})
		}
	}
	return out
}

// inspectMigrationApp verifies identities and intents before inventorying its storage.
func (r *Runtime) inspectMigrationApp(ctx context.Context, app appRecord) ([]MigrationVolume, error) {
	// Reject incompatible state before continuing the protected migration step.
	if !validMigrationSource(app) {
		return nil, fmt.Errorf("%s: migration requires a running, single-replica application without pending drain or autoscaling", app.Name)
	}
	intent, err := r.readIntent(app.Name)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	// Treat absent optional state explicitly rather than inferring authority.
	if intent != nil {
		return nil, fmt.Errorf("%s: resolve pending deployment intent before migration", app.Name)
	}
	// Complete every selected item before advancing the migration boundary.
	for _, id := range migrationContainerIDs(app) {
		// Preserve failure before advancing to the next external-effect boundary.
		if _, err := r.container(ctx, id, app.Name); err != nil {
			return nil, err
		}
	}
	out := []MigrationVolume{}
	// Complete every selected item before advancing the migration boundary.
	for _, spec := range migrationVolumeSpecs(app.Spec) {
		v, err := r.inspectMigrationVolume(ctx, app.Name, spec)
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// inspectMigrationVolume rejects remote drivers, external options and ownership changes.
func (r *Runtime) inspectMigrationVolume(ctx context.Context, app string, spec migrationVolumeSpec) (MigrationVolume, error) {
	v := MigrationVolume{Application: app, Name: "hostlier-" + r.owner + "-" + app + "-" + spec.key, Claim: spec.claim, Size: spec.volume.Size}
	var detail struct {
		Name, Driver, Mountpoint string
		Labels                   map[string]string
		Options                  map[string]string
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.api(ctx, "GET", "/volumes/"+url.PathEscape(v.Name), nil, &detail, nil); err != nil {
		return v, err
	}
	// Unknown ownership never grants authority over a runtime object.
	if !r.owned(detail.Labels, app) || detail.Name != v.Name || detail.Driver != "local" || len(detail.Options) != 0 || !filepath.IsAbs(detail.Mountpoint) {
		return v, fmt.Errorf("%s: migration requires an owned local volume without driver options", v.Name)
	}
	v.Path = detail.Mountpoint
	return v, nil
}

// migrationVolumeOwnership keeps this migration step within its validated recovery boundary.
func (r *Runtime) migrationVolumeOwnership(ctx context.Context, volumes []MigrationVolume) error {
	// Reject incompatible state before continuing the protected migration step.
	if len(volumes) == 0 {
		return nil
	}
	var containers []struct {
		Labels map[string]string
		Mounts []migrationMount
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.api(ctx, "GET", "/containers/json?all=true", nil, &containers, nil); err != nil {
		return err
	}
	// Verify each selected container under the current ownership scope.
	for _, c := range containers {
		// Complete every selected item before advancing the migration boundary.
		for _, mount := range c.Mounts {
			// Process every primary and sidecar volume within the same writer fence.
			for _, v := range volumes {
				uses := migrationMountUses(mount, v)
				// Unknown ownership never grants authority over a runtime object.
				if uses && !r.owned(c.Labels, v.Application) {
					return fmt.Errorf("%s: another application or unmanaged container can write the source volume", v.Name)
				}
			}
		}
	}
	return nil
}

// migrationMount identifies daemon-declared volume and host bind mounts.
type migrationMount struct{ Name, Type, Source string }

// migrationMountUses detects named volumes and broad binds that expose their host paths.
func migrationMountUses(m migrationMount, v MigrationVolume) bool {
	// Reject incompatible state before continuing the protected migration step.
	if m.Type != "bind" {
		return m.Name == v.Name
	}
	rel, err := filepath.Rel(m.Source, v.Path)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return false
	}
	return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sortedMigrationApps keeps this migration step within its validated recovery boundary.
func sortedMigrationApps(s state) []string {
	names := make([]string, 0, len(s.Apps))
	// Apply the identity checks to every application in the server-wide handoff.
	for name := range s.Apps {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// migrationContainerIDs keeps this migration step within its validated recovery boundary.
func migrationContainerIDs(app appRecord) []string {
	ids := []string{app.RuntimeID}
	// Complete every selected item before advancing the migration boundary.
	for _, member := range model.MemberNames(app.Spec) {
		ids = append(ids, app.Members[member])
	}
	// Reject incompatible state before continuing the protected migration step.
	if app.Anchor != "" {
		ids = append(ids, app.Anchor)
	}
	return ids
}

// MigrationFenceDocker stops every owned revision, including superseded containers,
// and disables automatic restart. The original policies are returned for recovery.
func (r *Runtime) MigrationFenceDocker(ctx context.Context, policies map[string]json.RawMessage, checkpoint func() error) error {
	state, err := r.read()
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	containers, err := r.migrationDockerContainers(ctx)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Verify each selected container under the current ownership scope.
	for _, c := range containers {
		name := c.Labels["io.hostlier.app"]
		app, ok := state.Apps[name]
		// Reject incompatible state before continuing the protected migration step.
		if !ok {
			return fmt.Errorf("untracked owned Docker application %s", name)
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err := r.fenceMigrationContainer(ctx, c, app, policies, checkpoint); err != nil {
			return err
		}
	}
	return r.verifyMigrationDockerStopped(ctx, containers)
}

// migrationDockerContainer carries only the identity needed for fencing.
type migrationDockerContainer struct {
	ID     string
	Labels map[string]string
}

// migrationDockerContainers includes superseded revisions, but leaves collectors separate.
func (r *Runtime) migrationDockerContainers(ctx context.Context) ([]migrationDockerContainer, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {"io.hostlier.managed=true", "io.hostlier.owner=" + r.owner}})
	var containers []migrationDockerContainer
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.api(ctx, "GET", "/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil, &containers, nil); err != nil {
		return nil, err
	}
	out := []migrationDockerContainer{}
	// Verify each selected container under the current ownership scope.
	for _, c := range containers {
		// Unknown ownership never grants authority over a runtime object.
		if c.Labels["io.hostlier.app"] != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// fenceMigrationContainer checkpoints the restart policy before disabling or stopping it.
func (r *Runtime) fenceMigrationContainer(ctx context.Context, c migrationDockerContainer, app appRecord, policies map[string]json.RawMessage, checkpoint func() error) error {
	// Unknown ownership never grants authority over a runtime object.
	if !r.owned(c.Labels, app.Name) {
		return fmt.Errorf("unowned Docker migration container")
	}
	var detail struct {
		HostConfig struct{ RestartPolicy json.RawMessage }
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.api(ctx, "GET", "/containers/"+c.ID+"/json", nil, &detail, nil); err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if _, ok := policies[c.ID]; !ok {
		policies[c.ID] = detail.HostConfig.RestartPolicy
		// Preserve failure before advancing to the next external-effect boundary.
		if err := checkpoint(); err != nil {
			return err
		}
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.api(ctx, "POST", "/containers/"+c.ID+"/update", map[string]any{"RestartPolicy": map[string]string{"Name": "no"}}, nil, nil); err != nil {
		return err
	}
	return r.groupContainerAction(ctx, app.Name, c.ID, "stop", model.GraceSeconds(app.Spec))
}

// verifyMigrationDockerStopped observes every writer after all stop requests complete.
func (r *Runtime) verifyMigrationDockerStopped(ctx context.Context, containers []migrationDockerContainer) error {
	// Verify each selected container under the current ownership scope.
	for _, c := range containers {
		detail, err := r.container(ctx, c.ID, c.Labels["io.hostlier.app"])
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		// Reject incompatible state before continuing the protected migration step.
		if detail.State.Running {
			return fmt.Errorf("Docker writer did not stop")
		}
	}
	return nil
}

// MigrationRestoreDocker starts only the committed source group, verifies probes,
// refreshes dynamic loopback ports, and restores the complete original edge.
func (r *Runtime) MigrationRestoreDocker(ctx context.Context, policies map[string]json.RawMessage) error {
	state, err := r.read()
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.startMigrationGroups(ctx, state); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.readyMigrationGroups(ctx, state); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.save(state); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.caddy(ctx, state); err != nil {
		return err
	}
	// Apply the identity checks to every application in the server-wide handoff.
	for _, app := range state.Apps {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := r.publicEndpointsReady(ctx, app.Spec); err != nil {
			return err
		}
	}
	return r.restoreMigrationPolicies(ctx, policies)
}

// startMigrationGroups brings up every provider before probing any consumer.
func (r *Runtime) startMigrationGroups(ctx context.Context, state state) error {
	order := migrationSourceOrder{runtime: r, ctx: ctx, state: state, visited: map[string]int{}}
	// Apply the identity checks to every application in the server-wide handoff.
	for _, name := range sortedMigrationApps(state) {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := order.start(name); err != nil {
			return err
		}
	}
	return nil
}

// migrationSourceOrder restores each private provider before starting its consumers.
type migrationSourceOrder struct {
	runtime *Runtime
	ctx     context.Context
	state   state
	visited map[string]int
}

// start establishes provider and sidecar readiness before granting the primary a writer.
func (order *migrationSourceOrder) start(name string) error {
	// Reject incompatible state before continuing the protected migration step.
	if order.visited[name] == 2 {
		return nil
	}
	// Reject incompatible state before continuing the protected migration step.
	if order.visited[name] == 1 {
		return fmt.Errorf("cyclic source workload bindings prevent rollback startup")
	}
	// Reject incompatible state before continuing the protected migration step.
	app, ok := order.state.Apps[name]
	// Reject incompatible state before continuing the protected migration step.
	if !ok {
		return fmt.Errorf("source provider is missing: %s", name)
	}
	order.visited[name] = 1
	// Complete every selected item before advancing the migration boundary.
	for _, provider := range migrationPrivateProviders(app.Spec) {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := order.start(provider); err != nil {
			return err
		}
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := order.runtime.startMigrationGroup(order.ctx, app); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := order.runtime.readyMigrationGroup(order.ctx, app); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := order.runtime.refreshMigrationPorts(order.ctx, &app); err != nil {
		return err
	}
	order.state.Apps[name] = app
	order.visited[name] = 2
	return nil
}

// migrationPrivateProviders keeps source startup order consistent with target deployment order.
func migrationPrivateProviders(p model.Project) []string {
	names := []string{}
	// Complete every selected item before advancing the migration boundary.
	for _, ref := range model.EnvironmentBindings(p) {
		parts := strings.Split(ref, ".")
		// Reject incompatible state before continuing the protected migration step.
		if len(parts) == 3 && parts[0] == "applications" {
			names = append(names, parts[1])
		}
	}
	sort.Strings(names)
	return names
}

// startMigrationGroup waits for required sidecars before starting the retained primary.
func (r *Runtime) startMigrationGroup(ctx context.Context, app appRecord) error {
	// Reject incompatible state before continuing the protected migration step.
	if app.Anchor != "" {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := r.groupContainerAction(ctx, app.Name, app.Anchor, "start", 20); err != nil {
			return err
		}
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.memberAction(ctx, app, "start"); err != nil {
		return err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.readyMigrationSidecars(ctx, app); err != nil {
		return err
	}
	return r.groupContainerAction(ctx, app.Name, app.RuntimeID, "start", 20)
}

// readyMigrationGroups verifies process probes and refreshes dynamic host ports.
func (r *Runtime) readyMigrationGroups(ctx context.Context, state state) error {
	// Apply the identity checks to every application in the server-wide handoff.
	for _, name := range sortedMigrationApps(state) {
		app := state.Apps[name]
		// Preserve failure before advancing to the next external-effect boundary.
		if err := r.readyMigrationGroup(ctx, app); err != nil {
			return err
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err := r.refreshMigrationPorts(ctx, &app); err != nil {
			return err
		}
		state.Apps[name] = app
	}
	return nil
}

// readyMigrationGroup preserves required-sidecar semantics during rollback.
func (r *Runtime) readyMigrationGroup(ctx context.Context, app appRecord) error {
	// Reject incompatible state before continuing the protected migration step.
	if app.Spec.Version != 2 {
		return nil
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := r.memberReady(ctx, app.Name, app.RuntimeID, app.Anchor, app.Spec); err != nil {
		return err
	}
	// Complete every selected item before advancing the migration boundary.
	return r.readyMigrationSidecars(ctx, app)
}

// readyMigrationSidecars retains optional-member semantics during source restoration.
func (r *Runtime) readyMigrationSidecars(ctx context.Context, app appRecord) error {
	// Complete every selected item before advancing the migration boundary.
	for _, member := range model.MemberNames(app.Spec) {
		spec := app.Spec.Sidecars[member]
		// Treat absent optional state explicitly rather than inferring authority.
		if spec.Required != nil && !*spec.Required {
			continue
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err := r.memberReady(ctx, app.Name, app.Members[member], app.Anchor, spec); err != nil {
			return err
		}
	}
	return nil
}

// refreshMigrationPorts prevents restored routes from retaining old ephemeral ports.
func (r *Runtime) refreshMigrationPorts(ctx context.Context, app *appRecord) error {
	// Reject incompatible state before continuing the protected migration step.
	if !model.Public(app.Spec) {
		return nil
	}
	id := app.RuntimeID
	// Reject incompatible state before continuing the protected migration step.
	if app.Anchor != "" {
		id = app.Anchor
	}
	port, err := r.backendPort(ctx, id, app.Spec.Port)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	app.Upstream = net.JoinHostPort(r.options.UpstreamHost, port)
	// Reject incompatible state before continuing the protected migration step.
	if app.Spec.Version == 1 {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := r.healthy(ctx, app.RuntimeID, port, healthPath(app.Spec), r.options.HealthTimeout); err != nil {
			return err
		}
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(app.Spec.Endpoints) > 0 {
		d := dockerDeployment{runtime: r, ctx: ctx, spec: app.Spec, anchor: app.Anchor, candidate: dockerContainer{ID: app.RuntimeID}}
		app.EndpointUpstreams, err = d.endpointUpstreams()
	}
	return err
}

// restoreMigrationPolicies runs only after data, readiness and routes are restored.
func (r *Runtime) restoreMigrationPolicies(ctx context.Context, policies map[string]json.RawMessage) error {
	// Restore each recorded restart policy after serving capacity is verified.
	for id, policy := range policies {
		var c dockerContainer
		// Preserve failure before advancing to the next external-effect boundary.
		if err := r.api(ctx, "GET", "/containers/"+url.PathEscape(id)+"/json", nil, &c, nil); err != nil {
			return err
		}
		// Unknown ownership never grants authority over a runtime object.
		if !r.owned(c.Config.Labels, "") {
			return fmt.Errorf("Docker rollback container ownership changed")
		}
		// Preserve failure before advancing to the next external-effect boundary.
		if err := r.api(ctx, "POST", "/containers/"+url.PathEscape(id)+"/update", map[string]any{"RestartPolicy": policy}, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// MigrationNamespace exposes the exact existing adapter naming contract.
func (k *K3s) MigrationNamespace(app string) string { return k.namespace(app) }

// MigrationPrepareVolume prebinds a retained, single-node local PV to the normal
// application PVC. Data is restored by the migration engine before this call.
func (k *K3s) MigrationPrepareVolume(ctx context.Context, v MigrationVolume, path, node, id string) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := k.namespaceReady(ctx, v.Application); err != nil {
		return err
	}
	name := "hostlier-migration-" + shortHash(id+v.Name)
	labels := k.base.labels(v.Application)
	pv := map[string]any{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": map[string]any{"name": name, "labels": labels}, "spec": map[string]any{
		"capacity": map[string]string{"storage": v.Size}, "volumeMode": "Filesystem", "accessModes": []string{"ReadWriteOnce"}, "persistentVolumeReclaimPolicy": "Retain", "storageClassName": "local-path",
		"local": map[string]string{"path": path}, "claimRef": map[string]string{"namespace": k.namespace(v.Application), "name": v.Claim},
		"nodeAffinity": map[string]any{"required": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "kubernetes.io/hostname", "operator": "In", "values": []string{node}}}}}}},
	}}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := k.apply(ctx, pv); err != nil {
		return err
	}
	return k.apply(ctx, map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": k.metadata(v.Application, v.Claim), "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "storageClassName": "local-path", "resources": map[string]any{"requests": map[string]string{"storage": v.Size}}}})
}

// MigrationFenceK3s removes application Deployments, waits for every Pod to be
// gone, and retains namespaces, PVCs, Secrets, and all data for inspection.
func (k *K3s) MigrationFenceK3s(ctx context.Context, names []string) error {
	// Complete every selected item before advancing the migration boundary.
	for _, name := range names {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := k.fenceMigrationNamespace(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// fenceMigrationNamespace waits for all Pods after deleting only owned Deployments.
func (k *K3s) fenceMigrationNamespace(ctx context.Context, name string) error {
	ns, err := k.get(ctx, "namespace", k.namespace(name), "")
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Treat absent optional state explicitly rather than inferring authority.
	if ns == nil {
		return nil
	}
	// Reject incompatible state before continuing the protected migration step.
	if !k.ownsApplication(ns, name) {
		return fmt.Errorf("Kubernetes migration namespace ownership changed")
	}
	deployments, err := k.migrationDeployments(ctx, name)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Complete every selected item before advancing the migration boundary.
	for _, deployment := range deployments {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := k.remove(ctx, "deployment", deployment, k.namespace(name)); err != nil {
			return err
		}
	}
	_, err = k.run(ctx, []string{"wait", "--for=delete", "pods", "--all", "-n", k.namespace(name), "--timeout=120s"}, nil)
	return err
}

// migrationDeployments independently checks every returned object's app ownership.
func (k *K3s) migrationDeployments(ctx context.Context, name string) ([]string, error) {
	var list struct{ Items []map[string]any }
	raw, err := k.run(ctx, []string{"get", "deployment", "-n", k.namespace(name), "-o", "json"}, nil)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	names := []string{}
	// Complete every selected item before advancing the migration boundary.
	for _, obj := range list.Items {
		// Reject incompatible state before continuing the protected migration step.
		if !k.ownsApplication(obj, name) {
			return nil, fmt.Errorf("unowned Deployment in migration namespace")
		}
		names = append(names, obj["metadata"].(map[string]any)["name"].(string))
	}
	return names, nil
}

// MigrationTargetEmpty prevents adopting any previous cluster application state.
func (k *K3s) MigrationTargetEmpty(ctx context.Context, names []string) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if _, err := os.Lstat(filepath.Join(k.options.StateDir, "runtime-k3s.json")); !os.IsNotExist(err) {
		return fmt.Errorf("target runtime metadata already exists")
	}
	// Complete every selected item before advancing the migration boundary.
	for _, name := range names {
		obj, err := k.get(ctx, "namespace", k.namespace(name), "")
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil {
			return err
		}
		// Treat absent optional state explicitly rather than inferring authority.
		if obj != nil {
			return fmt.Errorf("target namespace already exists: %s", k.namespace(name))
		}
	}
	return nil
}

// MigrationRestoreRoutes restores all committed target routes after an interruption.
func (k *K3s) MigrationRestoreRoutes(ctx context.Context) error {
	s, err := k.base.read()
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	return k.base.caddy(ctx, s)
}

// MigrationRetireTarget removes a fully fenced previous namespace. Its local PVs
// have Retain reclaim policy, so previous data and backup evidence remain intact.
func (k *K3s) MigrationRetireTarget(ctx context.Context, names []string) error {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := k.MigrationFenceK3s(ctx, names); err != nil {
		return err
	}
	// Complete every selected item before advancing the migration boundary.
	for _, name := range names {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := k.remove(ctx, "namespace", k.namespace(name), ""); err != nil {
			return err
		}
	}
	return nil
}
