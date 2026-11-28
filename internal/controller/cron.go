package controller

import (
	"encoding/json"
	"net/http"

	"hostlier/internal/model"
)

// checkCronDeploymentKind prevents an ordinary app from taking over a registered schedule by name.
func (c *Controller) checkCronDeploymentKind(spec model.Project) error {
	if app := c.state.Applications[spec.Name]; app != nil && (spec.Kind == model.CronKind) != (app.Spec.Kind == model.CronKind) {
		return model.Err("APP_KIND", "This name belongs to a different workload kind")
	}
	for _, op := range c.state.Operations {
		if op.Kind == "deploy" && op.Name == spec.Name && op.Status == "interrupted" && (spec.Kind == model.CronKind) != (op.Spec.Kind == model.CronKind) {
			return model.Err("APP_BUSY", "Resolve the interrupted deployment before changing the workload kind")
		}
	}
	return nil
}

func cronPlanChanges(spec model.Project, refs map[string]SecretRef, warnings []string) map[string]any {
	return map[string]any{
		"kind": model.CronKind, "runtime": "k3s", "schedule": spec.Schedule, "timeZone": spec.TimeZone,
		"image": spec.Image, "command": spec.Command, "args": spec.Args, "secrets": refs, "warnings": warnings,
		"resources": spec.Resources, "concurrencyPolicy": "Forbid",
		"runRetention": "Up to three successful and three failed runs, also expire after 24 hours",
	}
}

func cronReadPath(p []string) bool {
	_, tail, ok := cronEndpoint(p)
	return ok && (len(tail) == 0 || tail[0] == "runs" && (len(tail) == 1 || len(tail) == 3 && tail[2] == "logs"))
}

func cronEndpoint(p []string) (job string, tail []string, ok bool) {
	if len(p) < 4 || p[3] != "cron" {
		return "", nil, false
	}
	tail = p[4:]
	if len(tail) > 0 && tail[0] == "jobs" {
		if len(tail) < 2 || tail[1] == "" {
			return "", nil, false
		}
		return tail[1], tail[2:], true
	}
	return "", tail, true
}

func cronActionPath(p []string) (string, bool) {
	job, tail, ok := cronEndpoint(p)
	return job, ok && len(tail) == 1 && tail[0] == "action"
}

func cronTarget(app, job string) string {
	if job != "" {
		return app + "/" + job
	}
	return app
}

func cronSpec(app *Application, job string) (model.Project, bool) {
	if app == nil || app.Status == "deleted" {
		return model.Project{}, false
	}
	if job == "" {
		return app.Spec, app.Spec.Kind == model.CronKind
	}
	schedule, ok := app.Spec.Cron[job]
	return model.ServiceCronProject(app.Spec, schedule), ok
}

type cronActionRequest struct {
	Action   string `json:"action"`
	Revision string `json:"revision"`
}

func (c *Controller) apiCron(r *http.Request, p []string, app *Application, actor string) (any, error) {
	job, tail, ok := cronEndpoint(p)
	spec, exists := cronSpec(app, job)
	if !ok || !exists {
		return endpointNotFound()
	}
	runtime, ok := c.opts.Runtime.(model.CronRuntime)
	if !ok {
		return nil, model.Err("CONTROLLER_MODE", "Standalone cron jobs require K3s")
	}
	name := app.Name
	target := cronTarget(name, job)
	if _, action := cronActionPath(p); r.Method == "POST" && action {
		var input cronActionRequest
		if err := decode(r, &input); err != nil {
			return nil, err
		}
		switch input.Action {
		case "pause", "resume", "run", "delete":
		default:
			return nil, model.Err("INVALID_ACTION", "Choose pause, resume, run or delete")
		}
		if input.Revision == "" || input.Revision != app.Revision {
			return nil, model.Err("STALE_PLAN", "Cron schedule revision changed; request a new action")
		}
		if c.busy(name) {
			return nil, model.Err("APP_BUSY", "Cron schedule has an active operation")
		}
		return c.acceptOperation(&Operation{ID: random(), Kind: "cron-action", Action: input.Action,
			Name: name, SwarmID: app.SwarmID, Actor: actor, Status: "queued", CronRevision: app.Revision, CronJob: job})
	}
	if r.Method != "GET" || !cronReadPath(p) {
		return endpointNotFound()
	}
	swarmID := app.SwarmID
	if len(tail) == 0 {
		status := app.Status
		if job != "" {
			serviceRuntime, ok := runtime.(model.ServiceCronRuntime)
			if !ok {
				return nil, model.Err("CONTROLLER_MODE", "Service cron jobs require K3s")
			}
			c.mu.Unlock()
			status, err := serviceRuntime.ServiceCronStatus(r.Context(), target)
			c.mu.Lock()
			if err != nil {
				return nil, err
			}
			if c.state.Applications[name] != app || app.SwarmID != swarmID || app.Status == "deleted" {
				return nil, model.Err("STALE_PLAN", "Cron registration changed during the read")
			}
			return cronStatus(target, status, app.Revision, spec), nil
		}
		return cronStatus(target, status, app.Revision, spec), nil
	}
	if len(tail) == 1 {
		c.mu.Unlock()
		runs, err := runtime.CronRuns(r.Context(), target)
		c.mu.Lock()
		if c.state.Applications[name] != app || app.SwarmID != swarmID || app.Status == "deleted" {
			return nil, model.Err("STALE_PLAN", "Cron registration changed during the read")
		}
		return map[string]any{"runs": runs}, err
	}
	var err error
	r, err = logTailRequest(r)
	if err != nil {
		return nil, err
	}
	c.mu.Unlock()
	raw, err := runtime.CronLogs(r.Context(), target, tail[1], model.LogTail(r.Context()))
	c.mu.Lock()
	if err != nil {
		return nil, err
	}
	if c.state.Applications[name] != app || app.SwarmID != swarmID || app.Status == "deleted" {
		return nil, model.Err("STALE_PLAN", "Cron registration changed during the read")
	}
	return map[string]string{"text": boundedLogTail(c.redactResource(c.redact(raw)), r)}, nil
}

func cronStatus(name, status, revision string, spec model.Project) map[string]string {
	return map[string]string{"name": name, "status": status, "schedule": spec.Schedule, "timeZone": spec.TimeZone, "revision": revision}
}

func (c *Controller) executeCronDeployment(input operationInput) operationResult {
	runtime, ok := c.opts.Runtime.(model.CronRuntime)
	if !ok {
		return operationResult{err: model.Err("CONTROLLER_MODE", "Standalone cron jobs require K3s")}
	}
	op := input.operation
	deployment, err := runtime.DeployCron(c.ctx, op.Spec, model.DeployContext{OperationID: op.ID, Secrets: input.secrets, Previous: input.previous})
	return operationResult{deployment: deployment, err: err}
}

func (c *Controller) executeCronAction(op *Operation) (string, error) {
	runtime, ok := c.opts.Runtime.(model.CronRuntime)
	if !ok {
		return "", model.Err("CONTROLLER_MODE", "Standalone cron jobs require K3s")
	}
	return runtime.CronAction(c.ctx, cronTarget(op.Name, op.CronJob), op.Action, op.ID, op.CronRevision)
}

func (c *Controller) recordCronAction(op *Operation, runID string) {
	op.RunID = runID
	if op.CronJob != "" {
		return
	}
	if op.Action == "delete" {
		delete(c.state.Applications, op.Name)
		return
	}
	app := c.state.Applications[op.Name]
	if app == nil {
		return
	}
	switch op.Action {
	case "pause":
		app.Status = "paused"
	case "resume":
		app.Status = "scheduled"
	}
}

// cronActionReview includes the revision that supplies the template for a manual run.
func cronActionReview(app *Application, job string, request humanRequest, review map[string]any) (json.RawMessage, error) {
	spec, exists := cronSpec(app, job)
	if !exists {
		return nil, model.Err("NOT_FOUND", "Cron schedule not found")
	}
	var input cronActionRequest
	if err := json.Unmarshal(request.Body, &input); err != nil || input.Revision == "" || input.Revision != app.Revision {
		return nil, model.Err("STALE_PLAN", "Cron schedule revision changed; request a new action")
	}
	review["revision"] = app.Revision
	review["image"] = spec.Image
	review["schedule"] = spec.Schedule
	review["timeZone"] = spec.TimeZone
	review["command"] = spec.Command
	review["args"] = spec.Args
	return json.Marshal(review)
}
