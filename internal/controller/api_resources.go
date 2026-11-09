package controller

import (
	"net/http"
)

// apiResources routes authenticated requests for private resource management.
func (c *Controller) apiResources(r *http.Request, p []string, actor string) (any, error) {

	return c.resourceAPI(r, p)
}
