package controller

import (
	"net/http"
)

// apiResourcePlans routes authenticated requests for resource approval planning.
func (c *Controller) apiResourcePlans(r *http.Request, p []string, actor string) (any, error) {
	method := r.Method

	// Route allocation maintenance through its dedicated exact approval workflow.
	if method == "POST" && len(p) == 3 && p[2] == "resize" {
		return c.makeResourceResizePlan(r, actor)
	}
	// Accept resource planning only on the exact POST collection route.
	if method == "POST" && len(p) == 2 {
		return c.makeResourcePlan(r, actor)
	}

	return endpointNotFound()
}
