package controller

import (
	"net/http"
)

// apiPlans routes authenticated requests for deployment planning and approval.
func (c *Controller) apiPlans(r *http.Request, p []string, actor string) (any, error) {
	method := r.Method
	// A central controller may retrieve only the delegated actor's exact retained plan.
	if method == "GET" && len(p) == 3 {
		plan := c.state.Plans[p[2]]
		// Apply this check before granting authority or advancing protocol state.
		if plan == nil || plan.Actor != actor {
			return endpointNotFound()
		}
		return plan, nil
	}

	// Resolve a new immutable plan on the collection route.
	if method == "POST" && len(p) == 2 {
		return c.makePlan(r, actor)
	}
	// Apply a reviewed plan only through its explicit approval route.
	if method == "POST" && len(p) == 4 && p[3] == "apply" {
		return c.applyPlan(r, p[2], actor)
	}

	return endpointNotFound()
}
