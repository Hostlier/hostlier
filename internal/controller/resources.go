package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/internal/store"
)

// DependencyPlan pins a dependency specification and its explicit sharing or ownership intent.
type DependencyPlan struct {
	Name    string             `json:"name"`
	Source  string             `json:"source"`
	Spec    model.ResourceSpec `json:"spec"`
	Changes any                `json:"changes,omitempty"`
}

// ResourceRecord retains resource state and ownership without plaintext credentials.
type ResourceRecord struct {
	OwnerApplication string `json:"ownerApplication,omitempty"`
	OwnerDependency  string `json:"ownerDependency,omitempty"`
	model.Resource
	Spec model.ResourceSpec `json:"spec"`
}

// capableResources requires both a resource manager and dependency-capable application runtime.
func (c *Controller) capableResources() error {
	// Reject this path: Resource provisioning unavailable for this runtime.
	if c.opts.ResourceManager == nil || c.opts.ResourceManager.Name() != c.opts.Runtime.Name() {
		return model.Err("UNSUPPORTED_RESOURCES", "Resource provisioning unavailable for this runtime")
	}
	return nil
}

// resourceDependents finds active and accepted applications that still require a resource.
func (c *Controller) resourceDependents(name string) []string {
	out := []string{}
	// Include every retained application when enforcing the shared state invariant.
	for _, app := range c.state.Applications {
		// Deleted applications no longer keep private resources in use.
		if app.Status == "deleted" {
			continue
		}
		// Check each private resource reference held by the active application.
		for _, ref := range app.ResourceRefs {
			// Record an accepted dependency reference to the selected resource.
			if ref == name {
				out = append(out, app.Name)
				break
			}
		}
	}
	return out
}

// prepareResource normalizes a resource specification and resolves its immutable runtime plan.
func (c *Controller) prepareResource(ctx context.Context, input model.ResourceSpec) (model.ResourcePlan, error) {
	// Require runtime resource support before planning or managing private dependencies.
	if err := c.capableResources(); err != nil {
		return model.ResourcePlan{}, err
	}
	// Ordinary provisioning and application redeployment retain approved database allocation.
	if existing := c.state.Resources[input.Name]; existing != nil && input.CPU == "" && input.Memory == "" {
		// Advance only after the required recovery invariant holds.
		if input.Image == "" {
			input.Image = existing.Spec.Image
		}
		input.CPU = existing.Spec.CPU
		input.Memory = existing.Spec.Memory
	}
	plan, err := c.opts.ResourceManager.Plan(ctx, input)
	// Reject this path: Resource plan is unsupported or invalid.
	if err != nil {
		return plan, model.Err("INVALID_RESOURCE", "Resource plan is unsupported or invalid")
	}
	// Reject this path: Retained resource specification differs; upgrades unsupported.
	if existing := c.state.Resources[plan.Spec.Name]; existing != nil && !sameResourceSettings(existing.Spec, plan.Spec) {
		return plan, model.Err("RESOURCE_CONFLICT", "Retained resource specification differs; upgrades unsupported")
	}
	// Reject this path: Resource has active operation.
	if c.resourceBusy(plan.Spec.Name) {
		return plan, model.Err("RESOURCE_BUSY", "Resource has active operation")
	}
	return plan, nil
}

// prepareDependencies resolves private dependencies and validates bindings before deployment approval.
func (c *Controller) prepareDependencies(ctx context.Context, spec model.Project) (map[string]DependencyPlan, error) {
	out := map[string]DependencyPlan{}
	// Require the controller to accept this request before advancing the command workflow.
	if len(spec.Dependencies) == 0 {
		return out, nil
	}
	// Require runtime resource support before planning or managing private dependencies.
	if err := c.capableResources(); err != nil {
		return nil, err
	}
	capability, ok := c.opts.Runtime.(interface{ SupportsDependencies() bool })
	// Reject this path: Runtime cannot connect resource dependencies.
	if !ok || !capability.SupportsDependencies() {
		return nil, model.Err("UNSUPPORTED_DEPENDENCIES", "Runtime cannot connect resource dependencies")
	}
	// Resolve every approved dependency before allowing application runtime work.
	for key, dep := range spec.Dependencies {
		planned, err := c.prepareDependency(ctx, spec.Name, key, dep)
		// Reject the deployment plan when any required dependency cannot be resolved.
		if err != nil {
			return nil, err
		}
		out[key] = planned
	}
	return out, nil
}

// makeResourcePlan creates an actor-bound resource plan without generating credentials or provisioning.
func (c *Controller) makeResourcePlan(r *http.Request, actor string) (any, error) {
	var body resourcePlanRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	c.restorePlanImage(&body)
	planned, err := c.prepareResource(r.Context(), body.Spec)
	// Do not construct an approval plan from an invalid resource request.
	if err != nil {
		return nil, err
	}
	// Reject this path: Backup not found.
	if body.RestoreBackupID != "" {
		backup, exists := c.state.Backups[body.RestoreBackupID]
		// Reject this path: Backup not found.
		if !exists {
			return nil, model.Err("NOT_FOUND", "Backup not found")
		}
		// Reject this path: Restore requires a separate resource of the same engine type.
		if backup.Type != planned.Spec.Type || backup.Name == planned.Spec.Name {
			return nil, model.Err("INVALID_RESTORE", "Restore requires a separate resource of the same engine type")
		}
		// Reject this path: Resource restoration unavailable.
		if _, ok := c.opts.ResourceManager.(backupManager); !ok {
			return nil, model.Err("UNSUPPORTED_OPERATION", "Resource restoration unavailable")
		}
		// Reject this path: Restore target must be a new resource.
		if c.state.Resources[planned.Spec.Name] != nil {
			return nil, model.Err("RESOURCE_CONFLICT", "Restore target must be a new resource")
		}
		planned.Changes = map[string]any{"resource": planned.Changes, "restoreBackupId": backup.ID, "restoreFrom": backup.Name, "target": planned.Spec.Name}
	}
	plan := &Plan{RestoreBackupID: body.RestoreBackupID, ID: random(), Kind: "resource", ResourceSpec: &planned.Spec, Changes: planned.Changes, Actor: actor, ExpiresAt: c.opts.Now().Add(c.opts.PlanTTL).UnixMilli(), Generation: c.state.Generation}
	plan.Fingerprint = config.Fingerprint(plan)
	c.state.Plans[plan.ID] = plan
	return map[string]any{"id": plan.ID, "kind": "resource", "spec": planned.Spec, "changes": plan.Changes, "fingerprint": plan.Fingerprint, "expiresAt": plan.ExpiresAt, "generation": plan.Generation, "restoreBackupId": plan.RestoreBackupID}, nil
}

// resourceAPI routes private resource reads, backup requests and lifecycle actions.
func (c *Controller) resourceAPI(r *http.Request, p []string) (any, error) {
	// Require runtime resource support before planning or managing private dependencies.
	if err := c.capableResources(); err != nil {
		return nil, err
	}
	// Build the resource collection response without revealing credentials.
	if matches(r, p, "GET", 2, "") {
		// Monitoring inventories use retained metadata; only explicit detail pages inspect the runtime.
		if r.URL.Query().Get("observed") == "false" {
			return c.resourceMetadataInventory(r.Context())
		}
		return c.resourceInventory(r.Context())
	}
	// Reject this path: Resource not found.
	if len(p) < 3 {
		return nil, model.Err("NOT_FOUND", "Resource not found")
	}
	resource := c.state.Resources[p[2]]
	// Reject this path: Resource not found.
	if resource == nil {
		return nil, model.Err("NOT_FOUND", "Resource not found")
	}
	// Return safe observed state for one named resource.
	if matches(r, p, "GET", 3, "") {
		return c.resourceObserved(r.Context(), resource), nil
	}
	// Use the long-running backup workflow on the explicit backup route.
	if matches(r, p, "POST", 4, "backups") {
		return c.createBackup(r, resource)
	}
	// Return redacted diagnostics on the explicit log route.
	if matches(r, p, "GET", 4, "logs") {
		return c.resourceLogs(r, resource)
	}
	// Validate and accept lifecycle work on the explicit action route.
	if matches(r, p, "POST", 4, "action") {
		return c.resourceAction(r, resource)
	}
	return nil, model.Err("NOT_FOUND", "Endpoint not found")
}

// resourceInventory snapshots map membership before runtime calls release the state lock.
func (c *Controller) resourceInventory(ctx context.Context) (any, error) {
	out := []any{}
	resources, err := collectionValues[*ResourceRecord](ctx, c.store, "resources", store.CollectionQuery{})
	if err != nil {
		return nil, err
	}
	// Each observation releases the lock during I/O and returns with state protected again.
	for _, resource := range resources {
		out = append(out, c.resourceObserved(ctx, resource))
	}
	return map[string]any{"resources": out}, nil
}

// resourceObserved combines persisted resource metadata with safe runtime status.
func (c *Controller) resourceObserved(ctx context.Context, resource *ResourceRecord) map[string]any {
	copy := *resource
	// Return retained deletion metadata without querying a removed runtime instance.
	if resource.Status == "deleted" {
		return c.resourcePublic(resource)
	}
	name := resource.Name
	c.mu.Unlock()
	observed, err := c.opts.ResourceManager.Status(ctx, name)
	c.mu.Lock()
	// Use fresh runtime observations when inspection succeeds.
	if err == nil {
		// Use fresh nonempty status without masking a recorded provisioning failure.
		if observed.Status != "" && copy.Status != "failed" {
			copy.Status = observed.Status
		}
		// Preserve a nonempty runtime identity returned by the resource manager.
		if observed.RuntimeID != "" {
			copy.RuntimeID = observed.RuntimeID
		}
	}
	out := c.resourcePublic(&copy)
	// Expose a bounded availability marker when runtime inspection fails.
	if err != nil {
		out["runtimeStatus"] = "unavailable"
	}
	return out
}

// resourcePublic exposes resource metadata while omitting private credentials.
func (c *Controller) resourcePublic(resource *ResourceRecord) map[string]any {
	data, _ := json.Marshal(resource)
	out := map[string]any{}
	json.Unmarshal(data, &out)
	out["dependents"] = c.resourceDependents(resource.Name)
	return out
}

// resourceCredentials decrypts stable credentials or generates and encrypts them for a new resource.
func (c *Controller) resourceCredentials(name string, spec model.ResourceSpec) (model.ResourceCredentials, error) {
	var value model.ResourceCredentials
	// Require stable protected resource credentials before invoking runtime work.
	if encrypted := c.state.ResourceCredentials[name]; encrypted != "" {
		plain, err := c.store.Decrypt(encrypted)
		// Stop when encrypted protected data cannot be authenticated and decrypted.
		if err != nil {
			return value, err
		}
		err = json.Unmarshal([]byte(plain), &value)
		return value, err
	}
	username := "hostlier"
	// Use the Neo4j administrator account required by its runtime image.
	if spec.Type == "neo4j" {
		username = "neo4j"
	}
	value = model.ResourceCredentials{Username: username, Password: random(), Database: "app"}
	plain, _ := json.Marshal(value)
	encrypted, err := c.store.Encrypt(string(plain))
	// Never persist private data when encryption fails.
	if err != nil {
		return value, err
	}
	c.state.ResourceCredentials[name] = encrypted
	return value, c.store.Save(c.state)
}

// provisionResource persists resource identity and credentials around runtime provisioning.
func (c *Controller) provisionResource(ctx context.Context, spec model.ResourceSpec, id string, onEvent func(model.Event)) (model.Resource, model.ResourceCredentials, error) {
	c.mu.Lock()
	credentials, err := c.resourceCredentials(spec.Name, spec)
	// Persist the stable resource identity and credentials before invoking the runtime.
	if err == nil {
		// Record the resource before invoking provisioning so retries retain its identity.
		if c.state.Resources[spec.Name] == nil {
			c.state.Resources[spec.Name] = &ResourceRecord{Resource: model.Resource{Name: spec.Name, Type: spec.Type, Status: "provisioning"}, Spec: spec}
		}
		c.assignResourceOwner(c.state.Resources[spec.Name], spec.Name, id)
		err = c.store.Save(c.state)
	}
	c.mu.Unlock()
	// Do not claim resource readiness after a failed provisioning prerequisite.
	if err != nil {
		return model.Resource{}, credentials, err
	}
	resource, err := c.opts.ResourceManager.Provision(ctx, spec, model.ResourceContext{Credentials: credentials, OperationID: id, OnEvent: onEvent})
	// Return provisioning failures without reporting a running resource.
	if err != nil {
		c.mu.Lock()
		// An executable preflight rejection leaves the retained runtime untouched.
		if !resourcePackagePreflight(err) {
			c.state.Resources[spec.Name].Status = "failed"
		}
		c.store.Save(c.state)
		c.mu.Unlock()
		return resource, credentials, err
	}
	resource.Name = spec.Name
	resource.Type = spec.Type
	c.mu.Lock()
	existing := c.state.Resources[spec.Name]
	record := &ResourceRecord{Resource: resource, Spec: spec, OwnerApplication: existing.OwnerApplication, OwnerDependency: existing.OwnerDependency}
	c.assignResourceOwner(record, spec.Name, id)
	c.state.Resources[spec.Name] = record
	err = c.store.Save(c.state)
	c.mu.Unlock()
	return resource, credentials, err
}

// executeDependencies provisions approved dependencies and resolves their private environment bindings.
func (c *Controller) executeDependencies(ctx context.Context, op *Operation, onEvent func(model.Event)) (map[string]string, []string, map[string]string, error) {
	bindings := map[string]string{}
	networks := []string{}
	refs := map[string]string{}
	outputs := map[string]map[string]string{}
	// Resolve every approved dependency before allowing application runtime work.
	for key, dep := range op.Dependencies {
		resource, credentials, err := c.provisionResource(ctx, dep.Spec, op.ID, onEvent)
		// Stop deployment when an approved dependency cannot be provisioned.
		if err != nil {
			return nil, nil, nil, err
		}
		output, err := c.opts.ResourceManager.Connection(ctx, dep.Name, credentials)
		// Stop deployment when private connection values cannot be resolved.
		if err != nil {
			return nil, nil, nil, err
		}
		// Reject this path: Resource has no private application network.
		if resource.Network == "" {
			return nil, nil, nil, model.Err("RESOURCE_NETWORK_UNAVAILABLE", "Resource has no private application network")
		}
		outputs[key] = output
		networks = append(networks, resource.Network)
		refs[key] = dep.Name
	}
	// Resolve each approved binding from its dependency connection fields.
	for env, ref := range model.EnvironmentBindings(op.Spec) {
		// Private endpoints are resolved separately from database credentials.
		if strings.HasPrefix(ref, "applications.") || strings.HasPrefix(ref, "sidecars.") {
			continue
		}
		ref = strings.TrimPrefix(ref, "dependencies.")
		parts := strings.SplitN(ref, ".", 2)
		// Reject this path: Resource binding unavailable.
		if len(parts) != 2 || outputs[parts[0]][parts[1]] == "" {
			return nil, nil, nil, model.Err("RESOURCE_BINDING_UNAVAILABLE", "Resource binding unavailable")
		}
		bindings[env] = outputs[parts[0]][parts[1]]
	}
	return bindings, networks, refs, nil
}

// redactResource removes decrypted resource passwords from diagnostics.
func (c *Controller) redactResource(value string) string {
	// Consider every retained resource password when redacting diagnostics.
	for _, encrypted := range c.state.ResourceCredentials {
		plain, err := c.store.Decrypt(encrypted)
		// Skip credentials that cannot be authenticated and decrypted.
		if err != nil {
			continue
		}
		var credentials model.ResourceCredentials
		// Redact only successfully decoded nonempty private passwords.
		if json.Unmarshal([]byte(plain), &credentials) == nil && credentials.Password != "" {
			value = strings.ReplaceAll(value, credentials.Password, "[REDACTED]")
			value = strings.ReplaceAll(value, url.QueryEscape(credentials.Password), "[REDACTED]")
		}
	}
	return value
}

// resourceBusy protects resources used by unfinished provisioning or lifecycle work.
func (c *Controller) resourceBusy(name string) bool {
	// Advance only after the required recovery invariant holds.
	if c.recoveryReserved(name) {
		return true
	}
	// Inspect accepted operations as well as active state before allowing conflicting work.
	for _, op := range c.state.Operations {
		// Finished operations no longer reserve the resource.
		if op.Status != "queued" && op.Status != "running" {
			continue
		}
		// Protect a resource with its own active lifecycle or provisioning operation.
		if strings.HasPrefix(op.Kind, "resource") && op.Name == name {
			return true
		}
		// Resolve every approved dependency before allowing application runtime work.
		for _, dep := range op.Dependencies {
			// Protect a dependency needed by an accepted application deployment.
			if dep.Name == name {
				return true
			}
		}
	}
	return false
}

// inferResourceOwners recovers owned-resource associations from retained application revisions.
func (c *Controller) inferResourceOwners() {
	// Inspect accepted operations as well as active state before allowing conflicting work.
	for _, operation := range c.state.Operations {
		// Only application deployments establish dependency ownership.
		if operation.Kind != "deploy" {
			continue
		}
		// Resolve every approved dependency before allowing application runtime work.
		for key, dep := range operation.Dependencies {
			// Explicitly shared dependencies do not grant ownership.
			if dep.Source != "provision" {
				continue
			}
			resource := c.state.Resources[dep.Name]
			// Ignore resources that are no longer retained in inventory.
			if resource == nil {
				continue
			}
			// Recover ownership only when no explicit owner has already been recorded.
			if resource.OwnerApplication == "" {
				resource.OwnerApplication = operation.Spec.Name
				resource.OwnerDependency = key
				// Mark conflicting historical owners so accidental sharing remains blocked.
			} else if resource.OwnerApplication != operation.Spec.Name || resource.OwnerDependency != key {
				resource.OwnerApplication = "conflicting-recorded-owners"
				resource.OwnerDependency = ""
			}
		}
	}
}

// prepareDependency distinguishes explicit sharing from new app-owned resource provisioning.
func (c *Controller) prepareDependency(ctx context.Context, appName, key string, dep model.Dependency) (DependencyPlan, error) {
	name := appName + "-" + key
	// Validate explicitly shared resources rather than provisioning replacements.
	if dep.Source == "existing" {
		name = dep.Instance
		existing := c.state.Resources[name]
		// Reject this path: Existing resource has an active operation.
		if c.resourceBusy(name) {
			return DependencyPlan{}, model.Err("RESOURCE_BUSY", "Existing resource has an active operation")
		}
		// Reject this path: Existing dependency unavailable.
		if !availableDependency(existing, dep.Type) {
			return DependencyPlan{}, model.Err("RESOURCE_NOT_FOUND", "Existing dependency unavailable")
		}
		planned, err := c.prepareResource(ctx, existing.Spec)
		// Existing resources need a fresh executable approval after package installation or upgrade.
		if err != nil {
			return DependencyPlan{}, err
		}
		return DependencyPlan{Name: name, Source: "existing", Spec: planned.Spec, Changes: planned.Changes}, nil
	} else {
		// Reject this path: Provisioned dependency identity is already owned; choose another dependency name or explicitly reference an existing instance.
		if existing := c.state.Resources[name]; existing != nil && (existing.OwnerApplication != appName || existing.OwnerDependency != key) {
			return DependencyPlan{}, model.Err("RESOURCE_CONFLICT", "Provisioned dependency identity is already owned; choose another dependency name or explicitly reference an existing instance")
		}
		planned, err := c.prepareResource(ctx, model.ResourceSpec{Name: name, Type: dep.Type, Storage: dep.Storage})
		// Reject provisioning plans whose resource inputs or immutable images cannot be resolved.
		if err != nil {
			return DependencyPlan{}, err
		}
		return DependencyPlan{Name: name, Source: "provision", Spec: planned.Spec, Changes: planned.Changes}, nil
	}
}

// resourceLogs follows redacted logs for a named private resource.
func (c *Controller) resourceLogs(r *http.Request, resource *ResourceRecord) (any, error) {
	r, err := logTailRequest(r)
	// Validate snapshot limits before accessing resource logs.
	if err != nil {
		return nil, err
	}
	name := resource.Name
	c.mu.Unlock()
	text, err := c.opts.ResourceManager.Logs(r.Context(), name)
	c.mu.Lock()
	// Reject this path: Resource logs unavailable.
	if err != nil {
		return nil, model.Err("RESOURCE_FAILED", "Resource logs unavailable")
	}
	text = c.redactResource(c.redact(text))
	// Return only the newest bounded tail of resource logs.
	text = boundedLogTail(text, r)
	return map[string]string{"text": text}, nil
}

// resourceAction validates private resource identity before the shared lifecycle workflow.
func (c *Controller) resourceAction(r *http.Request, resource *ResourceRecord) (any, error) {
	var body actionRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	// Reject this path: Invalid resource action.
	if !validAction(body) {
		return nil, model.Err("INVALID_ACTION", "Invalid resource action")
	}
	// Reject this path: Active applications depend on resource.
	if (body.Action == "stop" || body.Action == "delete") && len(c.resourceDependents(resource.Name)) > 0 {
		return nil, model.Err("RESOURCE_IN_USE", "Active applications depend on resource")
	}
	// Reject this path: Resource has active operation or accepted deployment.
	if c.resourceBusy(resource.Name) {
		return nil, model.Err("RESOURCE_BUSY", "Resource has active operation or accepted deployment")
	}
	op := &Operation{ID: random(), Kind: "resource-action", Name: resource.Name, ResourceSpec: &resource.Spec, Action: body.Action, Purge: body.Purge, Status: "queued"}
	return c.acceptOperation(op)
}

// assignResourceOwner preserves ownership across ordinary redeployments without silently transferring it.
func (c *Controller) assignResourceOwner(record *ResourceRecord, name, id string) {
	// Assign ownership only from an accepted application deployment.
	if operation := c.state.Operations[id]; operation != nil && operation.Kind == "deploy" {
		// Resolve every approved dependency before allowing application runtime work.
		for key, dep := range operation.Dependencies {
			// Link ownership only to dependencies explicitly requested for provisioning.
			if dep.Name == name && dep.Source == "provision" {
				record.OwnerApplication = operation.Spec.Name
				record.OwnerDependency = key
			}
		}

	}
}

// resourcePlanRequest bounds the recognized JSON fields for this API action.
type resourcePlanRequest struct {
	Spec            model.ResourceSpec `json:"spec"`
	RestoreBackupID string             `json:"restoreBackupId"`
}

// availableDependency rejects missing, deleted, failed or mismatched dependency instances.
func availableDependency(resource *ResourceRecord, kind string) bool {
	return resource != nil && resource.Status != "deleted" && resource.Status != "failed" && resource.Type == kind
}

// sameResourceSettings excludes executable approval metadata from immutable resource identity.
func sameResourceSettings(a, b model.ResourceSpec) bool {
	a.PackageDigest = ""
	b.PackageDigest = ""
	return config.Fingerprint(a) == config.Fingerprint(b)
}

// resourcePackagePreflight identifies broker failures guaranteed to precede child execution.
func resourcePackagePreflight(err error) bool {
	var typed *model.Error
	return errors.As(err, &typed) && typed.Code == "RESOURCE_PACKAGE_PREFLIGHT"
}

// restorePlanImage retains the authenticated snapshot's immutable image when no explicit restore target was selected.
func (c *Controller) restorePlanImage(body *resourcePlanRequest) {
	// Advance only after the required recovery invariant holds.
	if backup, ok := c.state.Backups[body.RestoreBackupID]; ok && backup.Spec != nil && body.Spec.Image == "" {
		body.Spec.Image = backup.Spec.Image
	}
}

// resourceMetadataInventory exposes relationships and allocation without triggering per-row runtime probes.
func (c *Controller) resourceMetadataInventory(ctx context.Context) (any, error) {
	out := []any{}
	resources, err := collectionValues[*ResourceRecord](ctx, c.store, "resources", store.CollectionQuery{})
	if err != nil {
		return nil, err
	}
	// Public resource records contain dependency names and desired allocation, never decrypted credentials.
	for _, resource := range resources {
		row := c.resourcePublic(resource)
		row["runtimeStatus"] = "not_observed"
		out = append(out, row)
	}
	return map[string]any{"resources": out}, nil
}
