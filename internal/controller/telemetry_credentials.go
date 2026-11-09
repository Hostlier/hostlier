package controller

import (
	"net/http"
	"strings"
	"time"

	"hostlier/internal/model"
	"hostlier/internal/store"
)

// TelemetryCollector is a revocable ingest-only credential, never a management client.
type TelemetryCollector struct {
	Managed   bool      `json:"managed,omitempty"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Runtime   string    `json:"runtime"`
	Verifier  string    `json:"verifier,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
	Revoked   bool      `json:"revoked"`
}

// telemetryCredentials confines collector credential administration to the protected local socket.
func (c *Controller) telemetryCredentials(r *http.Request, local bool) (any, error) {
	// Only direct protected-socket administration may issue collector authority.
	if !local || r.Context().Value(fleetContext{}) != nil {
		return nil, model.Err("UNAUTHORIZED", "Collector credentials require local server administration")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Shutdown stops admission before any new diagnostic work begins.
	if c.closed {
		return nil, model.Err("UNAVAILABLE", "Controller is shutting down")
	}
	// Lists never return credential verifiers.
	if r.URL.Path == "/v1/telemetry/collectors" && r.Method == "GET" {
		return c.listTelemetryCollectors(r)
	}

	// Match the complete route before invoking its dedicated handler.
	if r.URL.Path == "/v1/telemetry/collectors" && r.Method == "POST" {
		return c.createTelemetryCollector(r)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Validate the precondition before continuing this telemetry operation.
	if matches(r, parts, "POST", 5, "revoke") {
		return c.revokeTelemetryCollector(parts[3])
	}
	return endpointNotFound()
}

// createTelemetryCollector persists only a verifier and returns the credential once.
func (c *Controller) createTelemetryCollector(r *http.Request) (any, error) {
	var input struct {
		Name string `json:"name"`
	}
	// Propagate failure without publishing an incomplete or misleading result.
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	// Require a bounded human-readable collector name before persistence.
	if required(input.Name) != nil {
		return nil, model.Err("INVALID_REQUEST", "Collector name is required")
	}
	original := c.state.TelemetryCollectors
	live := c.liveTelemetryCollectors()
	// Bound retained active credentials even when issuance is repeated.
	if len(live) >= 64 {
		return nil, model.Err("RATE_LIMITED", "Collector credential capacity reached")
	}
	c.state.TelemetryCollectors = live
	token := "hlt_" + random()
	record := TelemetryCollector{ID: random()[:24], Name: input.Name, Runtime: c.state.Runtime, Verifier: verifier(token), ExpiresAt: c.opts.Now().Add(30 * 24 * time.Hour)}
	c.state.TelemetryCollectors[record.ID] = record
	// Propagate failure without publishing an incomplete or misleading result.
	if err := c.store.Save(c.state); err != nil {
		c.state.TelemetryCollectors = original
		return nil, err
	}
	record.Verifier = ""
	return map[string]any{"collector": record, "token": token}, nil
}

// telemetryAuthority only accepts scoped collector credentials, not ordinary administrator tokens.
func (c *Controller) telemetryAuthority(r *http.Request) (TelemetryCollector, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Shutdown stops admission before any new diagnostic work begins.
	if c.closed {
		return TelemetryCollector{}, model.Err("UNAVAILABLE", "Controller is shutting down")
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Keep this credential and route namespace separate from management APIs.
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer hlt_") {
		return TelemetryCollector{}, model.Err("UNAUTHORIZED", "Collector credential required")
	}
	hash := verifier(token)
	// Visit the bounded inventory without serializing private credential material.
	for _, record := range c.state.TelemetryCollectors {
		// Only a live credential for this runtime may upload observations.
		if record.Verifier == hash && !record.Revoked && c.opts.Now().Before(record.ExpiresAt) && record.Runtime == c.state.Runtime {
			return record, nil
		}
	}
	return TelemetryCollector{}, model.Err("UNAUTHORIZED", "Collector credential expired or revoked")
}

// liveTelemetryCollectors allows bounded credential rotation without retaining expired verifiers forever.
func (c *Controller) liveTelemetryCollectors() map[string]TelemetryCollector {
	result := map[string]TelemetryCollector{}
	// The old map remains untouched until the replacement has been saved successfully.
	for id, record := range c.state.TelemetryCollectors {
		// Managed tombstones prevent a private checkpoint from resurrecting revoked authority; reconciliation retires them.
		if record.Managed || (!record.Revoked && c.opts.Now().Before(record.ExpiresAt)) {
			result[id] = record
		}
	}
	return result
}

// listTelemetryCollectors returns stable public credential metadata without verifiers.
func (c *Controller) listTelemetryCollectors(r *http.Request) (any, error) {
	out, err := collectionValues[TelemetryCollector](r.Context(), c.store, "telemetryCollectors", store.CollectionQuery{})
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Verifier = ""
	}
	return map[string]any{"collectors": out}, nil
}

// revokeTelemetryCollector changes authority only if the verifier update can be persisted.
func (c *Controller) revokeTelemetryCollector(id string) (any, error) {
	original, ok := c.state.TelemetryCollectors[id]
	// A nonexistent credential cannot be acknowledged as revoked.
	if !ok {
		return endpointNotFound()
	}
	updated := original
	updated.Revoked = true
	c.state.TelemetryCollectors[id] = updated
	// Roll back in-memory authority on failure so the caller receives an honest outcome.
	if err := c.store.Save(c.state); err != nil {
		c.state.TelemetryCollectors[id] = original
		return nil, err
	}
	return map[string]any{"revoked": id}, nil
}
