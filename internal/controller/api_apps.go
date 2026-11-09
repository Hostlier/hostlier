package controller

import (
	"context"
	"encoding/json"
	"net/http"

	"hostlier/internal/model"
	"hostlier/internal/store"
)

// apiApps routes authenticated requests for application status and lifecycle.
func (c *Controller) apiApps(r *http.Request, p []string, actor string) (any, error) {

	// Return the persisted application inventory.
	if matches(r, p, "GET", 2, "") {
		query := store.CollectionQuery{}
		if r.URL.Query().Get("includeDeleted") != "true" {
			query.ExcludeStatus = "deleted"
		}
		apps, err := collectionValues[*Application](r.Context(), c.store, "applications", query)
		return map[string]any{"applications": apps}, err
	}
	// Reject paths that cannot identify an application.
	if len(p) < 3 {
		return endpointNotFound()
	}
	app := c.state.Applications[p[2]]
	// Reject this path: Application not found.
	if app == nil {
		return nil, model.Err("NOT_FOUND", "Application not found")
	}
	// Return safe status for the exact application route.
	if matches(r, p, "GET", 3, "") {
		return c.applicationStatus(r, app)
	}
	// Usage measurements are read-only and scoped to an existing application.
	if matches(r, p, "GET", 4, "usage") {
		return c.applicationUsage(r, app)
	}
	// Return logs only on the explicit log route.
	if matches(r, p, "GET", 4, "logs") {
		return c.applicationLogs(r, app)
	}
	// Accept lifecycle work only on the explicit action route.
	if matches(r, p, "POST", 4, "action") {
		return c.applicationAction(r, app, actor)
	}

	return endpointNotFound()
}

// applicationStatus adds only allowlisted runtime diagnostics to persisted application metadata.
func (c *Controller) applicationStatus(r *http.Request, app *Application) (any, error) {
	// Add runtime diagnostics only when the adapter supports inspection.
	if inspector, ok := c.opts.Runtime.(runtimeInspector); ok {
		// Copy mutable metadata before releasing state for runtime I/O.
		data, _ := json.Marshal(app)
		name := app.Name
		c.mu.Unlock()
		diagnostics, err := inspector.Status(r.Context(), name)
		c.mu.Lock()
		// Add allowlisted diagnostics only after runtime inspection succeeds.
		if err == nil {
			out := map[string]any{}
			json.Unmarshal(data, &out)
			safe := map[string]any{}
			// Allowlist diagnostic fields so runtime details cannot expose secrets.
			for _, key := range []string{"idleSleeping", "running", "oomKilled", "exitCode", "status", "restartCount", "replicas", "readyReplicas"} {
				// Copy only fields present in the safe diagnostic allowlist.
				if value, exists := diagnostics[key]; exists {
					safe[key] = value
				}
			}
			// Only the typed diagnostic structure can add component detail to API responses.
			if components, ok := diagnostics["components"].([]model.ComponentStatus); ok {
				safe["components"] = components
			}
			out["runtimeStatus"] = safe
			return out, nil
		}
	}
	// A runtime call may have replaced the application's authoritative record.
	return c.state.Applications[app.Name], nil
}

// applicationLogs redacts secrets and bounds log responses before exposing runtime output.
func (c *Controller) applicationLogs(r *http.Request, app *Application) (any, error) {
	r, err := logTailRequest(r)
	// Validate snapshot limits before accessing runtime logs.
	if err != nil {
		return nil, err
	}
	name := app.Name
	c.mu.Unlock()
	raw, err := c.readComponentLogs(r, name)
	c.mu.Lock()
	// Reject this path: Application logs unavailable.
	if err != nil {
		return nil, model.Err("RUNTIME_FAILED", "Application logs unavailable")
	}
	raw = c.redactResource(c.redact(raw))
	// Return only the newest bounded tail of application logs.
	raw = boundedLogTail(raw, r)
	return map[string]string{"text": raw}, nil
}

// applicationAction validates lifecycle intent and queues work only when the application is idle.
func (c *Controller) applicationAction(r *http.Request, app *Application, actor string) (any, error) {
	var b actionRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	return c.queueAppAction(app, b.Action, b.Purge, actor)
}

// queueAppAction applies the shared lifecycle checks; the actor lets scoped devices follow their operation.
func (c *Controller) queueAppAction(app *Application, action string, purge bool, actor string) (any, error) {
	// Reject this path: Invalid application action.
	if !validAction(actionRequest{Action: action, Purge: purge}) {
		return nil, model.Err("INVALID_ACTION", "Invalid application action")
	}
	// Reject this path: Application has active operation.
	if c.busy(app.Name) {
		return nil, model.Err("APP_BUSY", "Application has active operation")
	}
	// Provider removal must not silently strand another running application.
	if err := c.validateProviderAction(app, action); err != nil {
		return nil, err
	}
	op := &Operation{ID: random(), Kind: "action", Name: app.Name, Action: action, Purge: purge, Status: "queued", Actor: actor}
	return c.acceptOperation(op)
}

// runtimeInspector supplies optional safe application diagnostics.
type runtimeInspector interface {
	Status(context.Context, string) (map[string]any, error)
}
