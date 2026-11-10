package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"hostlier/internal/model"
)

// handle serializes API state changes and persists successful requests before responding.
func (c *Controller) handle(w http.ResponseWriter, r *http.Request, local bool) {
	// Tunnel traffic never enters the management API or holds its lock while streaming.
	if c.servePublicSurface(w, r, local) {
		return
	}
	c.handleManagement(w, r, local)
}

// handleManagement is shared by the HTTP SDK and the CLI command RPC.
func (c *Controller) handleManagement(w http.ResponseWriter, r *http.Request, local bool) {
	// Independent reads perform their own authentication without retaining the aggregate lock.
	if c.serveUnserializedRead(w, r, local) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	// Extend the response deadline for bounded backup and export work.
	if strings.HasSuffix(r.URL.Path, "/backups") || strings.HasSuffix(r.URL.Path, "/export") || strings.HasSuffix(r.URL.Path, "/execute") || (r.Method == http.MethodPost && (r.URL.Path == "/v1/tunnels" || strings.HasSuffix(r.URL.Path, "/artifacts/pull"))) {
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(c.opts.LongOperationTimeout))
	}
	r.Body = http.MaxBytesReader(w, r.Body, apiBodyLimit(r, local))
	payload, err := c.dispatchResponse(r, local)
	// Translate failures into safe public error responses.
	if err != nil {
		public := &model.Error{Code: "REQUEST_FAILED", Message: "Request failed"}
		var e *model.Error
		// Preserve recognized typed errors while hiding arbitrary internal diagnostics.
		if errors.As(err, &e) {
			public = e
		}
		status := errorStatus(public.Code)
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": public})
		return
	}
	w.Write(append(payload, '\n'))
}

// apiBodyLimit keeps the larger HTTP allowance on local administration and
// the exact remote artifact chunk route.
func apiBodyLimit(r *http.Request, local bool) int64 {
	if local || artifactChunkRoute(r) {
		return maxRequestBytes
	}
	return 1 << 20
}

// dispatchResponse freezes mutable references before releasing the lock for response writes.
func (c *Controller) dispatchResponse(r *http.Request, local bool) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out, err := c.dispatch(r, local)
	// Read-only requests never write the aggregate or incur SQLite synchronization.
	if err == nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		err = c.store.Save(c.state)
		c.signalJobChange()
	}
	// Errors carry no state references and are serialized after releasing the lock.
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// endpointNotFound returns a consistent typed error for unsupported API routes.
func endpointNotFound() (any, error) { return nil, model.Err("NOT_FOUND", "Endpoint not found") }

// publicEndpoint identifies the minimal health and enrollment routes that need no bearer credential.
func publicEndpoint(r *http.Request, p []string) bool {
	// These endpoints verify hardware-key authority rather than trusting a bearer credential.
	if publicHumanEndpoint(r) || publicEmailEndpoint(r) || publicAccessEndpoint(r) {
		return true
	}
	// Allow unauthenticated health probes without exposing controller state.
	if r.Method == "GET" && r.URL.Path == "/health" {
		return true
	}
	// Keep every route except the exact enrollment entry points authenticated.
	if r.Method != "POST" || len(p) != 3 || p[0] != "v1" || p[1] != "pairing" {
		return false
	}
	return map[string]bool{"redeem": true, "requests": true, "poll": true}[p[2]]
}

// authenticate separates trusted local administration from remote bearer authentication.
func (c *Controller) authenticate(r *http.Request, local, public bool) (string, error) {
	actor := ""
	// Trust the protected local listener as the administrator identity.
	if local {
		actor = "local"
		// Only the pinned mTLS host listener can attach delegated actor authority.
		if authority, ok := r.Context().Value(fleetContext{}).(hostAuthority); ok {
			actor = authority.Actor
		}
		// Remote nonpublic routes must authenticate a live bearer credential.
	} else if !public {
		auth := ""
		// Accept credentials only from the explicit bearer authorization scheme.
		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			auth = strings.TrimPrefix(header, "Bearer ")
		}
		// Check enrolled client records while preserving credential privacy.
		for _, client := range c.state.Clients {
			// Match only live client credential verifiers.
			if !client.Revoked && client.Verifier == verifier(auth) {
				// Propagate failure rather than continuing with incomplete or unverified state.
				if err := c.verifyClientAccess(r, client); err != nil {
					return "", err
				}
				actor = client.ID
			}
		}
		// Reject this path: Authentication required.
		if actor == "" {
			return "", model.Err("UNAUTHORIZED", "Authentication required")
		}
	}

	return actor, nil
}

// enrollmentRate limits issuance and polling separately by source host, ignoring source ports.
func (c *Controller) enrollmentRate(r *http.Request, p []string) error {

	rateKey, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	// Use the source address directly when no host-port pair is available.
	if splitErr != nil {
		rateKey = r.RemoteAddr
	}
	limit := 60
	// Keep polling in its own larger budget so it cannot exhaust issuance quota.
	if len(p) == 3 && p[2] == "poll" {
		rateKey += "|poll"
		limit = 1200
	}
	entry := c.rates[rateKey]
	now := c.opts.Now()
	// Start a fresh request window after the previous minute expires.
	if now.Sub(entry.start) > 60000000000 {
		entry = rate{start: now}
	}
	entry.count++
	c.rates[rateKey] = entry
	// Reject this path: Enrollment rate limit exceeded.
	if entry.count > limit {
		return model.Err("RATE_LIMITED", "Enrollment rate limit exceeded")
	}
	// Bound in-memory rate state by evicting an entry when capacity is reached.
	if len(c.rates) > 10000 {
		// Evict one rate record to keep memory usage bounded.
		for key := range c.rates {
			delete(c.rates, key)
			break
		}
	}

	return nil
}

// dispatch authenticates and rate-limits requests before selecting their resource handler.
func (c *Controller) dispatch(r *http.Request, local bool) (any, error) {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	public := publicEndpoint(r, p)
	actor, err := c.authenticate(r, local, public)
	// Authenticate protected routes before exposing any state or selecting a handler.
	if err != nil {
		return nil, err
	}
	// Return a minimal response for public health probes.
	if r.URL.Path == "/health" && r.Method == "GET" {
		return map[string]any{"ok": true}, nil
	}
	// Reject routes outside the versioned API namespace.
	if len(p) < 2 || p[0] != "v1" {
		return endpointNotFound()
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := c.authorizeDispatch(r, p, public, local); err != nil {
		return nil, err
	}
	// Host generation is checked under the same lock as the protected mutation.
	if err := c.checkHostAuthority(r); err != nil {
		return nil, err
	}
	// Root-owned local administration is an explicit audited recovery boundary, never bearer authority.
	if actor == "local" && model.RequiresHumanApproval(r.Method, r.URL.Path) {
		c.log("Privileged local administration: "+r.Method+" "+r.URL.Path, "", "warn")
	}
	if r.URL.Path == "/v1/fleet/master" && local && actor == "local" {
		return c.localMaster(r)
	}
	return c.routeAPI(r, p, actor)
}

// routeAPI is reachable only after authentication/preparation checks or durable human approval consumption.
func (c *Controller) routeAPI(r *http.Request, p []string, actor string) (any, error) {
	result, err := c.routeAPIAction(r, p, actor)
	if err == nil && r.Method != "GET" && r.Method != "HEAD" {
		c.log("Request completed: "+r.Method+" "+r.URL.Path, "", "info")
		entry := &c.state.Events[len(c.state.Events)-1]
		entry.Actor = actor
		if len(p) > 2 && p[1] == "apps" {
			entry.Application = p[2]
		}
	}
	return result, err
}

// routeAPIAction admits maintenance reservations before selecting the protected operation.
func (c *Controller) routeAPIAction(r *http.Request, p []string, actor string) (any, error) {
	// Accepted workload mutations cannot race the detached controller installer.
	if err := c.checkMaintenanceMutation(r); err != nil {
		return nil, err
	}
	// Controller maintenance retains the same authenticated and approved dispatcher.
	if p[1] == "controller" {
		return c.apiControllerMaintenance(r, p, actor)
	}
	// Apply this check before granting authority or advancing protocol state.
	if p[1] == "authorizations" {
		return c.apiAuthorizations(r, p, actor)
	}
	// Apply this check before granting authority or advancing protocol state.
	if p[1] == "email" {
		return c.apiEmail(r, p, actor)
	}
	// Apply this check before granting authority or advancing protocol state.
	if r.Method == "GET" && len(p) == 2 && p[1] == "host-state" {
		return map[string]any{"generation": c.state.Generation, "hostname": c.state.Hostname}, nil
	}
	// Apply this check before granting authority or advancing protocol state.
	if p[1] == "hosts" {
		return c.apiHosts(r, p, actor)
	}
	handlers := map[string]func(*http.Request, []string, string) (any, error){"tunnels": c.apiTunnels, "fused": c.apiFused, "access": c.apiAccess, "packages": c.apiPackages, "package-uploads": c.apiPackageUploads, "backup-ticks": c.apiRecovery, "recovery-review": c.apiRecovery, "backup-destinations": c.apiRecovery, "backup-policies": c.apiRecovery, "recovery-jobs": c.apiRecovery, "upgrades": c.apiRecovery, "backup-imports": c.apiRecovery, "human": c.apiHuman, "approvals": c.apiApprovals, "planning": c.apiPlanning, "workflows": c.apiWorkflows, "artifacts": c.apiArtifacts, "pairing": c.apiPairing, "clients": c.apiClients, "secrets": c.apiSecrets, "backups": c.apiBackups, "resource-plans": c.apiResourcePlans, "resources": c.apiResources, "plans": c.apiPlans, "apps": c.apiApps, "status": c.apiStatus, "operations": c.apiOperations, "doctor": c.apiDoctor, "logs": c.apiLogs, "domains": c.apiDomains}
	// Dispatch only recognized API resource names.
	if handler := handlers[p[1]]; handler != nil {
		return handler(r, p, actor)
	}
	return endpointNotFound()
}

// emptyBody rejects unexpected payload fields on bodyless actions.
func emptyBody(r *http.Request) error {
	raw, err := io.ReadAll(r.Body)
	// Reject unreadable payloads before declaring an action body empty.
	if err != nil {
		return err
	}
	// Accept truly empty input on bodyless actions.
	if len(raw) == 0 {
		return nil
	}
	// Reject this path: Unexpected request fields.
	if strings.TrimSpace(string(raw)) != "{}" {
		return model.Err("INVALID_REQUEST", "Unexpected request fields")
	}
	return nil
}

// busy protects applications that already have queued or running work.
func (c *Controller) busy(name string) bool {
	// Inspect accepted operations as well as active state before allowing conflicting work.
	for _, o := range c.state.Operations {
		// Report only queued or running operations for the selected application.
		if o.Name == name && (o.Status == "queued" || o.Status == "running") {
			return true
		}
	}
	return false
}

// actionRequest bounds the recognized JSON fields for this API action.
type actionRequest struct {
	Action string `json:"action"`
	Purge  bool   `json:"purge"`
}

// validAction restricts lifecycle actions and permits purge only with deletion.
func validAction(b actionRequest) bool {
	allowed := map[string]bool{"stop": true, "start": true, "restart": true, "delete": true}
	return allowed[b.Action] && (!b.Purge || b.Action == "delete")
}

// acceptOperation persists accepted work before handing it to the background executor.
func (c *Controller) acceptOperation(op *Operation) (any, error) {
	c.state.Operations[op.ID] = op
	c.state.Generation++
	// Require durable state persistence before reporting success or scheduling side effects.
	if err := c.store.Save(c.state); err != nil {
		return nil, err
	}
	c.enqueue(op.ID)
	return map[string]string{"operationId": op.ID}, nil
}

// errorStatus maps public error categories to stable HTTP response codes.
func errorStatus(code string) int {
	status := 400
	// Map safe public errors to stable HTTP semantics.
	switch code {
	// Exporters must retry transient diagnostic storage failures.
	case "TELEMETRY_UNAVAILABLE":
		status = 503
	// Require authentication for unavailable bearer identities.
	case "UNAUTHORIZED":
		status = 401
	// Distinguish invalid enrollment and missing consent from ordinary request errors.
	case "INVALID_ENROLLMENT", "APPROVAL_REQUIRED", "HUMAN_APPROVAL_REQUIRED", "HUMAN_NOT_ENROLLED", "HUMAN_ORIGIN", "HUMAN_VERIFICATION", "APPROVAL_INVALID":
		status = 403
	// Hide unavailable or actor-inaccessible identities behind a not-found response.
	case "NOT_FOUND":
		status = 404
	// Report state conflicts so callers review current state before retrying.
	case "STALE_PLAN", "ROUTE_CONFLICT", "APP_BUSY", "SECRET_IN_USE", "IDEMPOTENCY_CONFLICT", "PLAN_CONSUMED", "DOMAIN_IN_USE", "RESOURCE_BUSY", "RESOURCE_IN_USE", "RESOURCE_CONFLICT", "CONFIG_CHANGED":
		status = 409
	// Tell callers to back off when enrollment capacity is exhausted.
	case "RATE_LIMITED", "ENROLLMENT_LIMIT":
		status = 429
	}

	return status
}

// matches checks the full route shape before a handler accesses path segments.
func matches(r *http.Request, p []string, method string, length int, tail string) bool {
	// Reject a method or path-length mismatch before inspecting its tail.
	if r.Method != method || len(p) != length {
		return false
	}
	return tail == "" || p[length-1] == tail
}

// authorizeDispatch rate-limits public ceremonies and prevents direct remote mutation.
func (c *Controller) authorizeDispatch(r *http.Request, p []string, public, local bool) error {
	// Apply issuance or polling limits before processing public enrollment traffic.
	if public && r.URL.Path != "/v1/email/session" {
		// Bound unauthenticated enrollment traffic before allocating or consuming challenge state.
		if err := c.enrollmentRate(r, p); err != nil {
			return err
		}
	}
	// Every protected mutation requires a server-retained, hardware-key-verified request.
	if !local && model.RequiresHumanApproval(r.Method, r.URL.Path) {
		return model.Err("HUMAN_APPROVAL_REQUIRED", "Submit this request through /v1/approvals for owner approval or a scoped deployment grant")
	}
	return nil
}

// servePublicSurface separates static and narrowly authenticated registry traffic from management APIs.
func (c *Controller) servePublicSurface(w http.ResponseWriter, r *http.Request, local bool) bool {
	return c.serveTunnel(w, r, local) || c.serveFusedCallback(w, r) || c.serveAccessPage(w, r) || c.serveCollectorRegistry(w, r)
}

// verifyClientAccess binds a signed request to both its device identity and deployment scope.
func (c *Controller) verifyClientAccess(r *http.Request, client *Client) error {
	// Scope checks cannot precede proof of possession of the registered device key.
	if err := c.verifyDeviceRequest(r, client); err != nil {
		return err
	}
	return c.checkScopedRoute(r, client)
}
