package controller

import (
	"encoding/json"
	"net/http"
	"slices"

	"hostlier/internal/config"
	"hostlier/internal/memory"
	"hostlier/internal/model"
)

// Storage controllers expose enrollment, review and storage lifecycle only.
// Package execution, workload provisioning, fleet scheduling and recovery jobs
// must not become alternate routes to the controller host filesystem.
func memoryAPISurface(resource string) bool {
	return slices.Contains([]string{"memory", "host-state", "human", "approvals", "pairing", "clients", "email", "access", "plans", "apps", "status", "operations", "doctor", "logs", "domains"}, resource)
}

func (c *Controller) makeMemoryPlan(r *http.Request, actor string) (any, error) {
	runtime, ok := c.opts.Runtime.(*memory.Runtime)
	if !ok {
		return nil, model.Err("CONTROLLER_MODE", "Memory exports require a memory controller")
	}
	if c.scopedClient(actor) {
		return nil, model.Err("SCOPE_DENIED", "Storage exports require owner authority")
	}
	var intent deploymentPlanRequest
	if err := decode(r, &intent); err != nil {
		return nil, err
	}
	if intent.Action != "" && intent.Action != "deploy" {
		return nil, model.Err("INVALID_ACTION", "Memory exports use deploy; host data is not rolled back")
	}
	spec, err := config.ValidateProject(intent.Spec)
	if err != nil {
		return nil, err
	}
	if spec.Kind != "memory" {
		return nil, model.Err("CONTROLLER_MODE", "Memory controllers only accept kind: memory")
	}
	if c.busy(spec.Name) {
		return nil, model.Err("APP_BUSY", "Export deployment has an active operation")
	}
	ids, err := runtime.Prepare(spec)
	if err != nil {
		return nil, err
	}
	plan := &Plan{ID: random(), Spec: spec, MemorySources: ids, Generation: c.state.Generation, Actor: actor, ExpiresAt: c.opts.Now().Add(c.opts.PlanTTL).UnixMilli(),
		Changes: map[string]any{"runtime": "memory", "exports": spec.Exports, "identities": ids, "retention": "Removing this deployment retains all host files", "isolation": "No symlinks, hard-linked files, special files or nested mounts"},
	}
	plan.Fingerprint = config.Fingerprint(plan)
	c.state.Plans[plan.ID] = plan
	copy := *plan
	copy.Actor = ""
	return &copy, nil
}
func (c *Controller) executeMemoryDeployment(op *Operation) operationResult {
	runtime, ok := c.opts.Runtime.(*memory.Runtime)
	if !ok {
		return operationResult{err: model.Err("CONTROLLER_MODE", "Memory controller required")}
	}
	deployment, err := runtime.Install(op.Spec, op.MemorySources, op.ID)
	return operationResult{deployment: deployment, err: err, deployed: err == nil}
}
func (c *Controller) initializeMemory() {
	if c.state.MemoryGrants == nil {
		c.state.MemoryGrants = map[string]*MemoryGrant{}
	}
	runtime, ok := c.opts.Runtime.(*memory.Runtime)
	if !ok {
		return
	}
	for _, app := range c.state.Applications {
		if app.Spec.Kind != "memory" || app.Status == "deleted" {
			continue
		}
		_, err := runtime.Install(app.Spec, app.MemorySources, app.Revision)
		if err != nil {
			app.Status = "unavailable"
			continue
		}
		if app.Status == "stopped" {
			_ = runtime.Action(c.ctx, app.Name, "stop", model.ActionContext{})
		}
	}
}

// MemoryGrant is an export-scoped data-plane identity, never a management login.
type MemoryGrant struct {
	ID        string `json:"id"`
	App       string `json:"app"`
	Export    string `json:"export"`
	Revision  string `json:"revision"`
	ReadOnly  bool   `json:"readOnly"`
	ExpiresAt int64  `json:"expiresAt"`
	Revoked   bool   `json:"revoked"`
	Secret    string `json:"secret"`
}

func (c *Controller) apiMemory(r *http.Request, p []string, actor string) (any, error) {
	if c.opts.Runtime.Name() != "memory" {
		return nil, model.Err("CONTROLLER_MODE", "Memory controller required")
	}
	if c.scopedClient(actor) {
		return nil, model.Err("SCOPE_DENIED", "Storage grants require owner authority")
	}
	if matches(r, p, "GET", 2, "") {
		return map[string]any{"mode": "memory", "protocols": []string{"webdav", "s3"}}, nil
	}
	if len(p) < 4 || p[3] != "grants" {
		return endpointNotFound()
	}
	app := c.state.Applications[p[2]]
	if app == nil || app.Spec.Kind != "memory" || app.Status == "deleted" {
		return endpointNotFound()
	}
	if matches(r, p, "GET", 4, "") {
		list := []map[string]any{}
		for _, g := range c.state.MemoryGrants {
			if g.App == app.Name {
				list = append(list, map[string]any{"id": g.ID, "export": g.Export, "readOnly": g.ReadOnly, "expiresAt": g.ExpiresAt, "revoked": g.Revoked, "revision": g.Revision})
			}
		}
		return map[string]any{"grants": list}, nil
	}
	if matches(r, p, "DELETE", 5, "") {
		g := c.state.MemoryGrants[p[4]]
		if g == nil || g.App != app.Name {
			return endpointNotFound()
		}
		g.Revoked = true
		c.state.Generation++
		return map[string]bool{"revoked": true}, nil
	}
	if matches(r, p, "POST", 4, "") {
		var input struct {
			Export          string `json:"export"`
			ReadOnly        *bool  `json:"readOnly"`
			DurationSeconds int64  `json:"durationSeconds"`
		}
		if err := decode(r, &input); err != nil {
			return nil, err
		}
		export, ok := app.Spec.Exports[input.Export]
		if !ok {
			return endpointNotFound()
		}
		if input.DurationSeconds < 60 || input.DurationSeconds > 30*24*60*60 {
			return nil, model.Err("INVALID_REQUEST", "Storage grants require durationSeconds from 60 to 2592000")
		}
		// Expired credentials have no remaining authority and need no retained secret.
		for id, g := range c.state.MemoryGrants {
			if g.ExpiresAt <= c.opts.Now().UnixMilli() {
				delete(c.state.MemoryGrants, id)
			}
		}
		if len(c.state.MemoryGrants) >= 1000 {
			return nil, model.Err("GRANT_LIMIT", "Too many storage grants")
		}
		secret := random()
		encrypted, err := c.store.Encrypt(secret)
		if err != nil {
			return nil, err
		}
		readOnly := input.ReadOnly == nil || *input.ReadOnly || export.ReadOnly
		g := &MemoryGrant{ID: random(), App: app.Name, Export: input.Export, Revision: app.Revision, ReadOnly: readOnly, ExpiresAt: c.opts.Now().UnixMilli() + input.DurationSeconds*1000, Secret: encrypted}
		c.state.MemoryGrants[g.ID] = g
		c.state.Generation++
		return map[string]any{"accessKeyId": g.ID, "secretAccessKey": secret, "username": g.ID, "password": secret, "readOnly": readOnly, "expiresAt": g.ExpiresAt,
			"webdavPath": "/memory/webdav/" + app.Name + "/" + input.Export + "/", "s3EndpointPath": "/memory/s3/" + app.Name, "bucket": input.Export, "region": "us-east-1"}, nil
	}
	return endpointNotFound()
}
func (c *Controller) memoryReview(request humanRequest, review map[string]any) {
	var input map[string]json.RawMessage
	if json.Unmarshal(request.Body, &input) == nil {
		for _, key := range []string{"export", "readOnly", "durationSeconds"} {
			if value, ok := input[key]; ok {
				review[key] = value
			}
		}
	}
}
