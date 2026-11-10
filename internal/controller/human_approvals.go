package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hostlier/internal/model"
)

// apiApprovals proposes, observes or executes exact independently approved requests.
func (c *Controller) apiApprovals(r *http.Request, p []string, actor string) (any, error) {
	// Creation records encrypted intent without invoking the protected handler.
	if matches(r, p, "POST", 2, "") {
		return c.proposeHumanRequest(r, actor)
	}
	// Every other operation names a request belonging to the authenticated proposer.
	if len(p) < 3 {
		return endpointNotFound()
	}
	a := c.state.HumanApprovals[p[2]]
	// Opaque URLs do not allow another API identity to execute or inspect the proposal.
	if a == nil || a.Actor != actor {
		return endpointNotFound()
	}
	// Polling exposes status and public review data, never stored private payloads.
	if matches(r, p, "GET", 3, "") {
		return c.humanApprovalView(a), nil
	}
	// Execute must consume independent approval before reaching a mutation handler.
	if matches(r, p, "POST", 4, "execute") {
		return c.executeHumanRequest(r, a)
	}
	return endpointNotFound()
}

// proposeHumanRequest binds the reviewed payload to actor, controller and state generation.
func (c *Controller) proposeHumanRequest(r *http.Request, actor string) (any, error) {
	var request humanRequest
	// Fail closed until a human has enrolled through the protected bootstrap ceremony.
	if !c.humanEnrolled() && !c.hasLiveAuthorization(actor) {
		return nil, model.Err("HUMAN_NOT_ENROLLED", "Enroll a browser passkey or hardware key, or obtain a scoped deployment credential")
	}
	// Reject unknown fields before recording request intent.
	if err := decode(r, &request); err != nil {
		return nil, err
	}
	// Only bounded canonical versioned API paths can be replayed internally.
	if err := validateHumanRequest(request); err != nil {
		return nil, err
	}
	c.expireHumanApprovals()
	if approval, err := c.recentCompletedSecretApproval(request, actor); err != nil {
		return nil, err
	} else if approval != nil {
		return c.humanApprovalView(approval), nil
	}
	// Under pressure, reclaim older completed receipts before refusing new work.
	// Keep recent receipts for response-loss recovery and never evict live reviews.
	if len(c.state.HumanApprovals) >= 1000 {
		c.reclaimCompletedApprovals()
	}
	// Bound pending and retained results to protect controller state from unbounded proposals.
	if len(c.state.HumanApprovals) >= 1000 {
		return nil, model.Err("APPROVAL_LIMIT", "Too many retained approval requests; wait for expiry")
	}
	return c.recordHumanRequest(request, actor)
}

// validateHumanRequest prevents nested authorization, URL rewriting and oversized payloads.
func validateHumanRequest(request humanRequest) error {
	u, err := url.ParseRequestURI(request.Path)
	// Canonical paths prevent proxy or router disagreement about the signed destination.
	if err != nil || u.RawQuery != "" || u.Path != request.Path || strings.Contains(request.Path, "..") || !strings.HasPrefix(request.Path, "/v1/") {
		return model.Err("INVALID_REQUEST", "Approval requires a canonical versioned API path")
	}
	methods := map[string]bool{"POST": true, "PUT": true, "DELETE": true, "GET": true}
	// Preparation and the approval protocol cannot themselves be wrapped as mutations.
	if !methods[request.Method] || !model.RequiresHumanApproval(request.Method, request.Path) || len(request.Body) > 100000 {
		return model.Err("INVALID_REQUEST", "Unsupported approval request")
	}
	return nil
}

// recordHumanRequest encrypts the original bytes and retains a secret-free review.
func (c *Controller) recordHumanRequest(request humanRequest, actor string) (any, error) {
	generation := c.state.Generation
	// Capture peer identity and state before generating the signed request meaning.
	if err := c.bindHostIntent(&request, actor); err != nil {
		return nil, err
	}
	data, err := json.Marshal(request)
	// Encoding failure must not leave an unusable approval entry.
	if err != nil {
		return nil, err
	}
	payload, err := c.store.Encrypt(string(data))
	// Never persist the original request in plaintext; it may contain a secret value.
	if err != nil {
		return nil, err
	}
	review, err := c.humanReview(request, actor)
	// Invalid deployment references cannot be presented as approvable work.
	if err != nil {
		return nil, err
	}
	// Remote inspection releases the lock, so reject any intervening central-state change.
	if c.state.Generation != generation {
		return nil, model.Err("STALE_PLAN", "Controller changed during host inspection; request a new review")
	}
	a := &HumanApproval{ID: random(), Code: strings.ToUpper(random()[:16]), Actor: actor, Method: request.Method, Path: request.Path, Payload: payload, Fingerprint: c.store.Authenticate(actor + "|" + c.state.Hostname + "|" + string(data)), Generation: c.state.Generation, ExpiresAt: c.opts.Now().Add(10 * time.Minute).UnixMilli(), Status: "pending", Review: review}
	c.applyAuthorization(a, request)
	// A scoped credential must never escalate to an unrestricted owner ceremony.
	if a.Status == "pending" && (c.scopedClient(actor) || !c.humanEnrolled()) {
		return nil, model.Err("SCOPE_DENIED", "Request exceeds this deployment credential; obtain a new explicit grant")
	}
	c.state.HumanApprovals[a.ID] = a
	c.log("Human approval requested: "+a.ID, "", "info")
	return c.humanApprovalView(a), nil
}

// humanReview publishes exact deployment plans and redacts private fields for other mutations.
func (c *Controller) humanReview(request humanRequest, actor string) (json.RawMessage, error) {
	review := map[string]any{"controller": c.state.Hostname, "method": request.Method, "path": request.Path, "requestedBy": actor, "warning": "This authorizes exactly this request. Review availability, credentials and data-retention consequences."}
	// Controller updates must show the verified retained release, not caller-supplied prose.
	if request.Path == "/v1/controller/update/apply" {
		return c.controllerUpdateReview(request, actor, review)
	}
	parts := strings.Split(strings.Trim(request.Path, "/"), "/")
	// Deployment review is bound to the server-retained plan rather than client-provided prose.
	if len(parts) == 4 && parts[1] == "plans" && parts[3] == "apply" {
		return c.humanPlanReview(request, actor, parts[2], review)
	}
	// Package installation must show actual server-verified bytes before approval.
	if packageInstallRequest(request) {
		return c.humanPackageReview(request, actor, review)
	}
	var body map[string]json.RawMessage
	// Non-object payloads cannot represent the existing mutation contracts.
	if len(request.Body) > 0 && json.Unmarshal(request.Body, &body) != nil {
		return nil, model.Err("INVALID_REQUEST", "Approval payload must be an object")
	}
	public := map[string]json.RawMessage{}
	// Expose only known nonsecret intent fields; all unknown fields remain private.
	for _, key := range []string{"bridgeId", "target", "connections", "engineUrl", "clientId", "clientSecretRef", "familyId", "secretName", "tokenName", "allow", "action", "purge", "name", "domain", "role", "host", "url", "fingerprint", "approved", "device", "apps", "appPrefix", "maxApps", "routeDomain", "keepApps", "secrets", "scope", "durationSeconds", "maxUses", "resource", "destination", "destinationFingerprint", "everySeconds", "retain", "to", "objectKey"} {
		// Retain exact JSON values without interpreting markup or instructions.
		if value, exists := body[key]; exists {
			public[key] = value
		}
	}
	review["changes"] = public
	c.addRecoveryReview(request, review)
	c.addTunnelReview(request, review)
	// Apply this check before granting authority or advancing protocol state.
	if _, _, ok := model.FleetPath(request.Path); ok {
		return c.reviewHostIntent(request, actor, review)
	}
	return json.Marshal(review)
}

// humanPlanReview rejects stale, cross-actor and altered plans before requesting a hardware-key.
func (c *Controller) humanPlanReview(request humanRequest, actor, id string, review map[string]any) (json.RawMessage, error) {
	plan := c.state.Plans[id]
	var supplied approvalRequest
	// The original actor and exact fingerprint remain mandatory in the independent approval flow.
	if plan == nil || plan.Actor != actor || json.Unmarshal(request.Body, &supplied) != nil || supplied.Fingerprint != plan.Fingerprint {
		return nil, model.Err("APPROVAL_REQUIRED", "The exact actor-owned plan is required")
	}
	// Expired or changed state requires a new plan before human review.
	if plan.Generation != c.state.Generation || plan.ExpiresAt <= c.opts.Now().UnixMilli() {
		return nil, model.Err("STALE_PLAN", "Plan expired or server changed; review a new plan")
	}
	review["plan"] = plan
	return json.Marshal(review)
}

// humanApprovalView never serializes encrypted payloads, credential identifiers or private results.
func (c *Controller) humanApprovalView(a *HumanApproval) map[string]any {
	return map[string]any{"id": a.ID, "status": a.Status, "fingerprint": a.Fingerprint, "expiresAt": a.ExpiresAt, "review": a.Review, "code": a.Code, "generation": a.Generation, "authorizationId": a.AuthorizationID}
}

// expireHumanApprovals bounds retained private requests while leaving active execution intact.
func (c *Controller) expireHumanApprovals() {
	// Retain receipts for a day after approval expiry to support ordinary response-loss recovery.
	for id, a := range c.state.HumanApprovals {
		// Active execution may still be completing a long operation and cannot lose its record.
		if a.Status != "executing" && a.ExpiresAt+int64(24*time.Hour/time.Millisecond) < c.opts.Now().UnixMilli() {
			delete(c.state.HumanApprovals, id)
		}
	}
}

func (c *Controller) reclaimCompletedApprovals() {
	cutoff := c.opts.Now().Add(-time.Hour).UnixMilli()
	for id, approval := range c.state.HumanApprovals {
		if approval.Status == "completed" && approval.ExpiresAt < cutoff {
			delete(c.state.HumanApprovals, id)
		}
	}
}

// executeHumanRequest consumes independent authorization durably before any protected side effect.
func (c *Controller) executeHumanRequest(r *http.Request, a *HumanApproval) (any, error) {
	// Execution has no caller-supplied replacement payload.
	if err := emptyBody(r); err != nil {
		return nil, err
	}
	// Completed responses are encrypted receipts, not a second execution.
	if a.Status == "completed" {
		return c.readHumanResult(a)
	}
	// A consumed or rejected approval must never start a new operation.
	if a.Status != "approved" || a.Approver == "" {
		return nil, model.Err("HUMAN_APPROVAL_REQUIRED", "This exact request needs owner approval or a scoped deployment grant")
	}
	// Any relevant state change invalidates unconsumed authorization.
	if a.Generation != c.state.Generation || a.ExpiresAt <= c.opts.Now().UnixMilli() {
		return nil, model.Err("STALE_PLAN", "Approval expired or server changed; request a new review")
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := c.validateExecutionAuthority(a); err != nil {
		return nil, err
	}
	request, err := c.openHumanRequest(a)
	// Corrupt or mismatched private intent cannot reach the operation handler.
	if err != nil {
		return nil, err
	}
	a.Status = "executing"
	// Crash recovery must see consumption before any external mutation begins.
	if err := c.store.Save(c.state); err != nil {
		a.Status = "approved"
		return nil, err
	}
	return c.runHumanRequest(r, request, a)
}

// openHumanRequest authenticates the retained payload against its immutable binding.
func (c *Controller) openHumanRequest(a *HumanApproval) (humanRequest, error) {
	var request humanRequest
	data, err := c.store.Decrypt(a.Payload)
	// Encryption and keyed intent authentication independently protect stored request meaning.
	if err != nil || c.store.Authenticate(a.Actor+"|"+c.state.Hostname+"|"+data) != a.Fingerprint {
		return request, model.Err("APPROVAL_INVALID", "Stored approval intent could not be authenticated")
	}
	// Decode the exact request recorded before the human ceremony.
	if err := json.Unmarshal([]byte(data), &request); err != nil {
		return request, err
	}
	return request, validateHumanRequest(request)
}

// runHumanRequest invokes the private dispatcher using only the retained request.
func (c *Controller) runHumanRequest(parent *http.Request, request humanRequest, a *HumanApproval) (any, error) {
	ctx := context.WithValue(parent.Context(), fleetContext{}, request.Target)
	ctx = context.WithValue(ctx, humanExecutionKey{}, a)
	r, err := http.NewRequestWithContext(ctx, request.Method, request.Path, bytes.NewReader(request.Body))
	// Request construction errors leave a consumed record for investigation.
	if err != nil {
		return nil, err
	}
	result, err := c.routeAPI(r, strings.Split(strings.Trim(request.Path, "/"), "/"), a.Actor)
	// Failed mutations remain consumed because an external effect may have occurred.
	if err != nil {
		a.Status, a.Failure = "failed", "Execution failed; inspect state before submitting a new request"
		c.store.Save(c.state)
		return nil, err
	}
	return c.finishHumanRequest(a, result)
}

// finishHumanRequest retains encrypted replay output, including newly issued credentials.
func (c *Controller) finishHumanRequest(a *HumanApproval, result any) (any, error) {
	data, err := json.Marshal(result)
	// A serialization failure cannot turn execution into reusable approval.
	if err != nil {
		return nil, err
	}
	a.Result, err = c.store.Encrypt(string(data))
	// Sensitive API responses are never persisted without encryption.
	if err != nil {
		return nil, err
	}
	a.Status = "completed"
	c.log("Human-approved request completed: "+a.ID, "", "info")
	return result, nil
}

// readHumanResult decodes a previously completed response without repeating side effects.
func (c *Controller) readHumanResult(a *HumanApproval) (any, error) {
	data, err := c.store.Decrypt(a.Result)
	// A missing or unreadable receipt requires investigation rather than mutation replay.
	if err != nil {
		return nil, err
	}
	var value any
	err = json.NewDecoder(io.NopCloser(strings.NewReader(data))).Decode(&value)
	return value, err
}

// validateExecutionAuthority rechecks revocation at execution, not only when a signature was received.
func (c *Controller) validateExecutionAuthority(a *HumanApproval) error {
	// Delegated approvals retain their original grant and device binding.
	if a.AuthorizationID != "" {
		return c.validateGrantExecution(a)
	}
	// Reject the request before side effects: Approver authority unavailable.
	if c.state.Human == nil || c.state.Human.ID != a.Approver {
		return model.Err("HUMAN_VERIFICATION", "Approver authority unavailable")
	}
	valid := c.liveApprovalKey(a.CredentialID)
	// Reject the request before side effects: Approver key revoked or unavailable.
	if !valid {
		return model.Err("HUMAN_VERIFICATION", "Approver key revoked or unavailable")
	}
	return nil
}
