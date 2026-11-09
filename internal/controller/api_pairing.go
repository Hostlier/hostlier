package controller

import (
	"net/http"

	"hostlier/internal/model"
	"hostlier/internal/store"
)

// apiPairing routes authenticated requests for enrollment administration.
func (c *Controller) apiPairing(r *http.Request, p []string, actor string) (any, error) {
	method := r.Method

	// List public challenge metadata without polling secrets or verifiers.
	if len(p) == 2 && method == "GET" {
		requests := []any{}
		rows, err := collectionValues[Enrollment](r.Context(), c.store, "requests", store.CollectionQuery{})
		if err != nil {
			return nil, err
		}
		for _, e := range rows {
			requests = append(requests, map[string]any{"id": e.ID, "name": e.Name, "status": e.Status, "expiresAt": e.ExpiresAt, "consumed": e.Consumed, "cancelled": e.Cancelled})
		}
		return map[string]any{"requests": requests}, nil
	}
	// Reject malformed enrollment routes before selecting an action.
	if len(p) != 3 || method != "POST" {
		return endpointNotFound()
	}

	handlers := map[string]func(*http.Request, []string) (any, error){"tokens": c.pairingTokens, "redeem": c.pairingRedeem, "requests": c.pairingRequests, "inspect": c.pairingInspect, "approve": c.pairingInspect, "poll": c.pairingPoll, "cancel": c.pairingCancel}
	// Invoke only recognized enrollment actions.
	if handler := handlers[p[2]]; handler != nil {
		return handler(r, p)
	}
	return endpointNotFound()
}

// pairingTokens creates an expiring one-time enrollment token for administrator delivery.
func (c *Controller) pairingTokens(r *http.Request, p []string) (any, error) {
	var b pairingNameRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	// Require a nonempty bounded identity before continuing.
	if err := required(b.Name); err != nil {
		return nil, err
	}
	t := random()
	expires := c.opts.Now().Add(c.opts.PairingTTL).UnixMilli()
	c.state.Enrollments[verifier(t)] = &Enrollment{ID: verifier(t), Name: b.Name, ExpiresAt: expires}
	c.log("Enrollment token created", "", "info")
	return map[string]any{"token": t, "expiresAt": expires, "url": "https://" + c.state.Hostname}, nil
}

// pairingRedeem consumes a valid one-time token and returns a new client credential.
func (c *Controller) pairingRedeem(r *http.Request, p []string) (any, error) {
	var b pairingRedeemRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	// Require a nonempty bounded identity before continuing.
	if err := required(b.Name); err != nil {
		return nil, err
	}
	e := c.state.Enrollments[verifier(b.Token)]
	// Reject consumed, cancelled or expired enrollment before issuing credentials.
	if err := c.valid(e); err != nil {
		return nil, err
	}
	e.Consumed = true
	c.log("Client enrolled", "", "info")
	return c.credentials(b.Name), nil
}

// pairingRequests creates a bounded pending challenge with distinct approval and polling secrets.
func (c *Controller) pairingRequests(r *http.Request, p []string) (any, error) {
	var b pairingNameRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	// Require a nonempty bounded identity before continuing.
	if err := required(b.Name); err != nil {
		return nil, err
	}
	// Keep outstanding enrollment challenges within the configured state bound.
	if err := c.limitPendingEnrollments(); err != nil {
		return nil, err
	}
	id, code, secret := random(), random()[:16], random()
	expires := c.opts.Now().Add(c.opts.PairingTTL).UnixMilli()
	c.state.Requests[id] = &Enrollment{ID: id, Name: b.Name, Verifier: verifier(secret), CodeHash: verifier(code), Status: "pending", ExpiresAt: expires}
	return map[string]any{"requestId": id, "code": code, "requestSecret": secret, "expiresAt": expires}, nil
}

// pairingInspect reveals the requested identity or approves a valid challenge.
func (c *Controller) pairingInspect(r *http.Request, p []string) (any, error) {
	var b pairingCodeRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	var found *Enrollment
	// Examine enrollment challenges without exposing their private verification material.
	for _, e := range c.state.Requests {
		// Find a challenge by its approval-code verifier.
		if e.CodeHash == verifier(b.Code) {
			found = e
		}
	}
	// Reject consumed, cancelled or expired enrollment before issuing credentials.
	if err := c.valid(found); err != nil {
		return nil, err
	}
	// Inspect identity without implicitly granting enrollment approval.
	if p[2] == "inspect" {
		return map[string]any{"id": found.ID, "name": found.Name, "status": found.Status, "expiresAt": found.ExpiresAt}, nil
	}
	found.Status = "approved"
	c.log("Enrollment approved", "", "info")
	return map[string]any{"approved": true}, nil
}

// pairingPoll requires the private polling secret before consuming approved enrollment.
func (c *Controller) pairingPoll(r *http.Request, p []string) (any, error) {
	var b pairingPollRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	e := c.state.Requests[b.RequestID]
	// Reject consumed, cancelled or expired enrollment before issuing credentials.
	if err := c.valid(e); err != nil {
		return nil, err
	}
	// Reject this path: Enrollment invalid or expired.
	if e.Verifier != verifier(b.RequestSecret) {
		return nil, model.Err("INVALID_ENROLLMENT", "Enrollment invalid or expired")
	}
	// Return pending enrollment status without issuing a credential.
	if e.Status != "approved" {
		return map[string]any{"status": e.Status}, nil
	}
	e.Consumed = true
	out := c.credentials(e.Name)
	out["status"] = "complete"
	return out, nil
}

// pairingCancel revokes an existing enrollment challenge without revealing its secrets.
func (c *Controller) pairingCancel(r *http.Request, p []string) (any, error) {
	var b pairingIDRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	e := c.state.Requests[b.ID]
	// Look for administrator-issued tokens when no client-requested challenge matches.
	if e == nil {
		e = c.state.Enrollments[b.ID]
	}
	// Reject this path: Enrollment not found.
	if e == nil {
		return nil, model.Err("NOT_FOUND", "Enrollment not found")
	}
	e.Cancelled = true
	return map[string]any{"cancelled": true}, nil
}

// limitPendingEnrollments prunes invalid challenges and bounds outstanding enrollment state.
func (c *Controller) limitPendingEnrollments() error {
	active := 0
	// Prune obsolete challenge records before the registry becomes too large.
	if len(c.state.Requests) >= 1000 {
		// Examine enrollment challenges without exposing their private verification material.
		for key, e := range c.state.Requests {
			// Remove challenges that can no longer issue credentials.
			if c.valid(e) != nil {
				delete(c.state.Requests, key)
			}
		}
	}
	// Examine enrollment challenges without exposing their private verification material.
	for _, e := range c.state.Requests {
		// Count only challenges that can still be approved or consumed.
		if c.valid(e) == nil {
			active++
		}
	}
	// Reject this path: Too many pending requests.
	if active >= 100 {
		return model.Err("ENROLLMENT_LIMIT", "Too many pending requests")
	}

	return nil
}

// pairingNameRequest bounds the recognized JSON fields for this API action.
type pairingNameRequest struct {
	Name string `json:"name"`
}

// pairingRedeemRequest bounds the recognized JSON fields for this API action.
type pairingRedeemRequest struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

// pairingCodeRequest bounds the recognized JSON fields for this API action.
type pairingCodeRequest struct {
	Code string `json:"code"`
}

// pairingPollRequest bounds the recognized JSON fields for this API action.
type pairingPollRequest struct {
	RequestID     string `json:"requestId"`
	RequestSecret string `json:"requestSecret"`
}

// pairingIDRequest bounds the recognized JSON fields for this API action.
type pairingIDRequest struct {
	ID string `json:"id"`
}
