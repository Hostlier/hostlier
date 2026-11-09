package controller

import (
	"hostlier/internal/store"
	"net/http"
)

// apiStatus routes authenticated requests for controller status.
func (c *Controller) apiStatus(r *http.Request, p []string, actor string) (any, error) {
	method := r.Method

	// Return the controller status snapshot only on the exact GET route.
	if method == "GET" && len(p) == 2 {
		apps, err := collectionValues[*Application](r.Context(), c.store, "applications", store.CollectionQuery{ExcludeStatus: "deleted"})
		if err != nil {
			return nil, err
		}
		operations, err := collectionValues[*Operation](r.Context(), c.store, "operations", store.CollectionQuery{})
		return map[string]any{"generation": c.state.Generation, "runtime": c.state.Runtime, "hostname": c.state.Hostname, "applications": apps, "operations": operations}, err
	}

	return endpointNotFound()
}
