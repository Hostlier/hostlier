package controller

import (
	"hostlier/internal/model"
	"net/http"
)

// applicationUsage returns observed measurements alongside the last approved resource settings.
func (c *Controller) applicationUsage(r *http.Request, app *Application) (any, error) {
	observer, ok := c.opts.Runtime.(model.UsageRuntime)
	// Optional runtime adapters can explicitly report that they cannot measure usage.
	if !ok {
		return model.Usage{Reason: "Runtime does not provide usage measurements", Source: c.opts.Runtime.Name(), Limits: app.Spec.Resources, DesiredReplicas: model.DesiredReplicas(app.Spec), Status: app.Status}, nil
	}
	name := app.Name
	limits := app.Spec.Resources
	replicas := model.DesiredReplicas(app.Spec)
	c.mu.Unlock()
	usage, err := observer.Usage(r.Context(), name)
	c.mu.Lock()
	// Runtime diagnostics may contain private paths or provider credentials.
	if err != nil {
		return nil, model.Err("RUNTIME_FAILED", "Application usage unavailable")
	}
	usage.Limits = limits
	usage.DesiredReplicas = replicas
	return usage, nil
}
