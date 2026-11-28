package controller

import (
	"fmt"
	"slices"

	"hostlier/internal/model"
)

func (c *Controller) supportsServiceCrons() bool {
	_, ok := c.opts.Runtime.(model.ServiceCronRuntime)
	return ok && c.opts.Runtime.Name() == "k3s"
}

func (c *Controller) reconcileServiceCrons(spec model.Project, dc model.DeployContext) error {
	runtime, ok := c.opts.Runtime.(model.ServiceCronRuntime)
	if !ok {
		return model.Err("CONTROLLER_MODE", "Service cron jobs require K3s")
	}
	if err := runtime.ReconcileServiceCrons(c.ctx, spec, dc); err != nil {
		return fmt.Errorf("%w: %w", model.Err("CRON_RECONCILE", "Could not apply service cron changes"), err)
	}
	return nil
}

// hasServiceCrons keeps cleanup in scope after an interrupted deployment or partial reconcile.
func (c *Controller) hasServiceCrons(name string) bool {
	if c.state.Version < 5 {
		return false
	}
	if app := c.state.Applications[name]; app != nil && app.Status != "deleted" && (len(app.Spec.Cron) != 0 || app.CronReconcilePending) {
		return true
	}
	for _, op := range c.state.Operations {
		if op.Name == name && op.Kind == "deploy" && op.Status == "interrupted" && len(op.Spec.Cron) != 0 {
			return true
		}
	}
	return false
}

func (c *Controller) serviceCronChanges(spec model.Project) map[string]any {
	if len(spec.Cron) == 0 && !c.hasServiceCrons(spec.Name) {
		return nil
	}
	remove := []string{}
	if app := c.state.Applications[spec.Name]; app != nil && app.Status != "deleted" {
		for job := range app.Spec.Cron {
			if _, desired := spec.Cron[job]; !desired {
				remove = append(remove, job)
			}
		}
	}
	slices.Sort(remove)
	return map[string]any{"jobs": spec.Cron, "remove": remove, "image": spec.Image}
}
