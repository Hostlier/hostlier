package controller

import (
	"net/http"
)

// apiDoctor routes authenticated requests for runtime diagnostics.
func (c *Controller) apiDoctor(r *http.Request, p []string, actor string) (any, error) {
	method := r.Method

	// Invoke diagnostics only on the exact authenticated GET route.
	if method == "GET" && len(p) == 2 {
		c.mu.Unlock()
		defer c.mu.Lock()
		return c.opts.Runtime.Doctor(r.Context())
	}

	return endpointNotFound()
}
