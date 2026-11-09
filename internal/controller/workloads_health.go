package controller

import (
	"context"
	"time"
)

// groupHealthReconciler is implemented when a backend needs controller-driven route withdrawal.
type groupHealthReconciler interface {
	ReconcileHealth(context.Context) (map[string]string, error)
}

// startGroupHealthMonitor continuously observes required Docker members while Kubernetes uses Pod readiness.
func (c *Controller) startGroupHealthMonitor() {
	monitor, ok := c.opts.Runtime.(groupHealthReconciler)
	// Kubernetes and legacy test adapters own their readiness behavior independently.
	if !ok {
		return
	}
	c.wg.Add(1)
	go func() { // The controller lifecycle owns and cancels this single bounded monitor.
		defer c.wg.Done()
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		// Wait between observations instead of competing with deployment execution continuously.
		for {
			// Shutdown takes priority over the next periodic observation.
			select {
			case <-c.ctx.Done(): // Cancellation terminates the controller-owned monitor.
				return // Shutdown never leaves a background runtime writer behind.
			case <-ticker.C:
				c.reconcileGroupHealth(monitor) // Observe current readiness under the ordinary execution lock.
			}
		}
	}()
}

// reconcileGroupHealth serializes observed availability with approved deployment operations.
func (c *Controller) reconcileGroupHealth(monitor groupHealthReconciler) {
	c.execMu.Lock()
	defer c.execMu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	changes, err := monitor.ReconcileHealth(ctx)
	// An unsuccessful runtime observation cannot update controller state.
	if err != nil || len(changes) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Runtime identities are scoped by the execution lock to the current deployment revision.
	for name, status := range changes {
		// Removed applications must not be recreated by a late observation.
		if app := c.state.Applications[name]; app != nil {
			app.Status = status
		}
	}
	c.state.Generation++
	_ = c.store.Save(c.state)
}
