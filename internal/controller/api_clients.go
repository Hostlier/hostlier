package controller

import (
	"net/http"

	"hostlier/internal/model"
	"hostlier/internal/store"
)

// apiClients routes authenticated requests for client revocation.
func (c *Controller) apiClients(r *http.Request, p []string, actor string) (any, error) {
	method := r.Method

	// List client identity and revocation state without credential verifiers.
	if method == "GET" && len(p) == 2 {
		clients, err := collectionValues[Client](r.Context(), c.store, "clients", store.CollectionQuery{})
		if err != nil {
			return nil, err
		}
		for i := range clients {
			clients[i].Verifier = ""
			clients[i].EnrollmentEmailHash = ""
		}
		return map[string]any{"clients": clients}, nil
	}
	// Require the controller to accept this request before advancing the command workflow.
	if method == "POST" && len(p) == 4 && p[3] == "revoke" {
		// Reject unexpected payload fields on this bodyless action.
		if err := emptyBody(r); err != nil {
			return nil, err
		}
		cl := c.state.Clients[p[2]]
		// Reject this path: Client not found.
		if cl == nil {
			return nil, model.Err("NOT_FOUND", "Client not found")
		}
		// Revoke the device's managed SSH credential before acknowledging device revocation.
		if c.opts.SSHAccess != nil {
			if err := c.opts.SSHAccess.Revoke(r.Context(), p[2]); err != nil {
				return nil, err
			}
		}
		cl.Revoked = true
		c.log("Client revoked", "", "info")
		return map[string]any{"revoked": true}, nil
	}

	return endpointNotFound()
}
