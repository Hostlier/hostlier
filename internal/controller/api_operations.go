package controller

import (
	"net/http"

	"hostlier/internal/model"
)

// apiOperations routes authenticated requests for durable operation status.
func (c *Controller) apiOperations(r *http.Request, p []string, actor string) (any, error) {
	method := r.Method

	// Read the durable status of the explicitly identified operation.
	if method == "GET" && len(p) == 3 {
		op := c.state.Operations[p[2]]
		// Reject this path: Operation not found.
		if op == nil {
			return nil, model.Err("NOT_FOUND", "Operation not found")
		}
		return op, nil
	}

	return endpointNotFound()
}
