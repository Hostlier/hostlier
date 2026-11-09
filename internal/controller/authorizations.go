package controller

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"hostlier/internal/model"
	"hostlier/internal/store"
)

// Authorization grants one device bounded deployment authority after a fresh hardware signature.
type Authorization struct {
	ID              string   `json:"id"`
	Device          string   `json:"device"`
	Host            string   `json:"host"`
	HostFingerprint string   `json:"hostFingerprint,omitempty"`
	Apps            []string `json:"apps"`
	Secrets         []string `json:"secrets,omitempty"`
	CreateSecrets   []string `json:"createSecrets,omitempty"`
	Scope           string   `json:"scope"`
	ExpiresAt       int64    `json:"expiresAt"`
	MaxUses         int      `json:"maxUses"`
	Uses            int      `json:"uses"`
	KeyFingerprint  string   `json:"keyFingerprint"`
	Revoked         bool     `json:"revoked"`
	AppPrefix       string   `json:"appPrefix,omitempty"`
	MaxApps         int      `json:"maxApps,omitempty"`
	RouteDomain     string   `json:"routeDomain,omitempty"`
	KeepApps        bool     `json:"keepApps,omitempty"`
	SandboxApps     []string `json:"sandboxApps,omitempty"`
	TornDown        bool     `json:"tornDown,omitempty"`
}

// humanExecutionKey carries independently verified provenance only inside the private dispatcher.
type humanExecutionKey struct{}

// authorizationRequest contains explicit scope and finite lifetime/usage bounds.
type authorizationRequest struct {
	Device          string   `json:"device"`
	Host            string   `json:"host"`
	Apps            []string `json:"apps"`
	Secrets         []string `json:"secrets,omitempty"`
	CreateSecrets   []string `json:"createSecrets,omitempty"`
	Scope           string   `json:"scope"`
	DurationSeconds int64    `json:"durationSeconds"`
	MaxUses         int      `json:"maxUses"`
	AppPrefix       string   `json:"appPrefix,omitempty"`
	MaxApps         int      `json:"maxApps,omitempty"`
	RouteDomain     string   `json:"routeDomain,omitempty"`
	KeepApps        bool     `json:"keepApps,omitempty"`
}

// apiAuthorizations permits creation through owner approval or a live all-access credential.
func (c *Controller) apiAuthorizations(r *http.Request, p []string, actor string) (any, error) {
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "GET", 3, "self") {
		return map[string]bool{"active": c.hasLiveAuthorization(actor)}, nil
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "GET", 2, "") {
		grants, err := collectionMap[*Authorization](r.Context(), c.store, "authorizations", store.CollectionQuery{})
		return map[string]any{"authorizations": grants}, err
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "POST", 2, "") {
		return c.createAuthorization(r)
	}
	// Revoke only the retained grant named by an independently approved request.
	if matches(r, p, "POST", 4, "revoke") {
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err := emptyBody(r); err != nil {
			return nil, err
		}
		grant := c.state.Authorizations[p[2]]
		// Apply this check before granting authority or advancing protocol state.
		if grant == nil {
			return endpointNotFound()
		}
		grant.Revoked = true
		c.state.Generation++
		c.log("Authorization revoked: "+grant.ID, "", "warn")
		return map[string]bool{"revoked": true}, nil
	}
	return endpointNotFound()
}

// createAuthorization records the approving hardware key so key revocation also revokes delegated authority.
func (c *Controller) createAuthorization(r *http.Request) (any, error) {
	approval, _ := r.Context().Value(humanExecutionKey{}).(*HumanApproval)
	// Restricted deployment windows cannot issue additional authority.
	if !c.canGrantAuthority(approval) {
		return nil, model.Err("HUMAN_VERIFICATION", "Owner approval or an all-access credential is required to grant authority")
	}
	var input authorizationRequest
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := c.validateAuthorization(input); err != nil {
		return nil, err
	}
	// Evaluate each retained grant without expanding its signed scope.
	for id, old := range c.state.Authorizations {
		// Apply this check before granting authority or advancing protocol state.
		if old.ExpiresAt != 0 && old.ExpiresAt+int64(24*time.Hour/time.Millisecond) < c.opts.Now().UnixMilli() {
			delete(c.state.Authorizations, id)
		}
	}
	// Reject the request before side effects: Retained authorization limit reached.
	if len(c.state.Authorizations) >= 1000 {
		return nil, model.Err("AUTHORIZATION_LIMIT", "Retained authorization limit reached")
	}
	pin := ""
	// Keep protected local recovery authority separate from remote credentials.
	if input.Host != "local" {
		pin = c.state.Hosts[input.Host].Fingerprint
	}
	grant := &Authorization{ID: random(), Device: input.Device, Host: input.Host, HostFingerprint: pin, Apps: input.Apps, Secrets: input.Secrets, CreateSecrets: input.CreateSecrets, Scope: input.Scope, ExpiresAt: c.opts.Now().Add(time.Duration(input.DurationSeconds) * time.Second).UnixMilli(), MaxUses: input.MaxUses, KeyFingerprint: approval.CredentialID, AppPrefix: input.AppPrefix, MaxApps: input.MaxApps, RouteDomain: input.RouteDomain, KeepApps: input.KeepApps}
	c.state.Authorizations[grant.ID] = grant
	c.state.Generation++
	c.log("Bounded deployment authority granted: "+grant.ID, "", "info")
	return grant, nil
}

// validateAuthorization requires explicit device, host, application and finite authority limits.
func (c *Controller) validateAuthorization(input authorizationRequest) error {
	device := c.state.Clients[input.Device]
	// Reject the request before side effects: Select an enrolled device with a bound public key.
	if device == nil || device.Revoked || device.PublicKey == "" {
		return model.Err("INVALID_AUTHORIZATION", "Select an enrolled device with a bound public key")
	}
	// Reject the request before side effects: Deployment authority requires 1–20 apps, 1–100 uses, and a duration from one minute to eight hours.
	if !validAuthorizationLimits(input) {
		return model.Err("INVALID_AUTHORIZATION", "Deployment authority requires 1–20 apps or an app prefix, 1–100 uses, and a duration from one minute to eight hours")
	}
	// Reject the request before side effects: Select local or an enrolled host.
	if err := c.validateAuthorizationHost(input.Host); err != nil {
		return err
	}
	// Require every referenced secret to fall inside the signed scope.
	for _, name := range append(append([]string{}, input.Apps...), input.Secrets...) {
		// Reject the request before side effects: Scope identifiers must be exact names without wildcards.
		if required(name) != nil || strings.ContainsAny(name, "*/\\") {
			return model.Err("INVALID_AUTHORIZATION", "Scope identifiers must be exact names without wildcards")
		}
	}
	// Sandbox windows carry their own name, capacity and route bounds.
	if err := validateSandboxRequest(input); err != nil {
		return err
	}
	return validateSecretCreationScope(deploymentScope{Secrets: input.Secrets, CreateSecrets: input.CreateSecrets})
}

// validateAuthorizationHost accepts the local runtime or an enrolled host.
func (c *Controller) validateAuthorizationHost(host string) error {
	// Remote windows require an enrolled host identity.
	if host != "local" {
		// Reject the request before side effects: Select local or an enrolled host.
		if _, ok := c.state.Hosts[host]; !ok {
			return model.Err("INVALID_AUTHORIZATION", "Select local or an enrolled host")
		}
	}
	return nil
}

// authorizationLive checks immediate revocation, expiry, device and approver-key authority.
func (c *Controller) authorizationLive(grant *Authorization) bool {
	// Fail closed when the required identity, scope or format does not match.
	if grant == nil || grant.Revoked || !c.authorizationUnexpired(grant) {
		return false
	}
	device := c.state.Clients[grant.Device]
	// Fail closed when the required identity, scope or format does not match.
	if device == nil || device.Revoked || !c.deviceEmailAllowed(device) {
		return false
	}
	return c.liveApprovalKey(grant.KeyFingerprint)
}

// applyAuthorization uses the approved all-access or bounded deployment grant.
func (c *Controller) applyAuthorization(a *HumanApproval, request humanRequest) {
	// Full authority still follows the retained approval and execution checks.
	if c.applyAllAccessAuthorization(a) {
		return
	}
	// Creation grants cannot authorize updates or other secret operations.
	if c.applySecretCreationAuthorization(a) {
		return
	}
	// Sandbox windows may stop, start, restart or delete only the apps they created.
	if c.applySandboxAction(a, request) {
		return
	}
	plan, host, ok := c.scopedPlan(a, request)
	// Apply this check before granting authority or advancing protocol state.
	if !ok {
		return
	}
	// Evaluate each retained grant without expanding its signed scope.
	for _, grant := range c.state.Authorizations {
		// Apply this check before granting authority or advancing protocol state.
		if !c.grantMatches(grant, a.Actor, host, plan.Spec.Name) || !grantAllowsPlan(grant, plan, request) {
			continue
		}
		grant.Uses++
		c.claimSandboxApp(grant, plan.Spec.Name)
		c.approveWithGrant(a, grant)
		c.log("Deployment authorized by bounded grant: "+grant.ID, "", "info")
		return
	}
}

// grantAllowsPlan checks host pinning, secret exposure and sandbox routes for one plan.
func grantAllowsPlan(grant *Authorization, plan Plan, request humanRequest) bool {
	// Remote hosts remain pinned to the enrolled identity.
	if request.Target != nil && grant.HostFingerprint != request.Target.Fingerprint {
		return false
	}
	return allowedPlanSecrets(plan, grantSecrets(grant, plan.Spec.Name)) && sandboxRoutesAllowed(grant, plan.Spec)
}

// approveWithGrant records delegated approval provenance for later execution checks.
func (c *Controller) approveWithGrant(a *HumanApproval, grant *Authorization) {
	a.Status = "approved"
	a.Approver = grant.KeyFingerprint
	a.CredentialID = grant.KeyFingerprint
	a.AuthorizationID = grant.ID
	a.ApprovedAt = c.opts.Now().UnixMilli()
}

// scopedPlan excludes database, workflow, dependency-provisioning and persistent-volume changes.
func (c *Controller) scopedPlan(a *HumanApproval, request humanRequest) (Plan, string, bool) {
	host, inner, isHost := model.FleetPath(request.Path)
	// Apply this check before granting authority or advancing protocol state.
	if !isHost {
		host = "local"
		inner = request.Path
	}
	parts := strings.Split(strings.Trim(inner, "/"), "/")
	// Apply this check before granting authority or advancing protocol state.
	if request.Method != "POST" || len(parts) != 4 || parts[1] != "plans" || parts[3] != "apply" {
		return Plan{}, host, false
	}
	var review struct {
		Plan Plan `json:"plan"`
	}
	// Apply this check before granting authority or advancing protocol state.
	if json.Unmarshal(a.Review, &review) != nil {
		return Plan{}, host, false
	}
	p := review.Plan
	return p, host, statelessPlan(p)
}

// allowedPlanSecrets prevents a deployment window from exposing credentials outside its explicit scope.
func allowedPlanSecrets(plan Plan, allowed []string) bool {
	// Require every referenced secret to fall inside the signed scope.
	for _, ref := range plan.SecretRefs {
		// Fail closed when the required identity, scope or format does not match.
		if !containsName(allowed, ref.Name) {
			return false
		}
	}
	return true
}

// containsName performs exact identity matching with no glob or prefix expansion.
func containsName(values []string, name string) bool {
	// Validate each entry before accepting the complete collection.
	for _, value := range values {
		// Apply this check before granting authority or advancing protocol state.
		if value == name {
			return true
		}
	}
	return false
}

// hasLiveAuthorization is an advisory prompt hint; exact scope is enforced when proposing execution.
func (c *Controller) hasLiveAuthorization(actor string) bool {
	// Evaluate each retained grant without expanding its signed scope.
	for _, grant := range c.state.Authorizations {
		// Apply this check before granting authority or advancing protocol state.
		if grant.Device == actor && c.authorizationLive(grant) && (grant.Scope == "all" || grant.Uses < grant.MaxUses) {
			return true
		}
	}
	return false
}

// validAuthorizationLimits caps both the duration and the maximum damage of delegated authority.
func validAuthorizationLimits(input authorizationRequest) bool {
	return !(input.Scope != "deploy" || len(input.Apps) == 0 && input.AppPrefix == "" || len(input.Apps) > 20 || len(input.Secrets) > 20 || input.DurationSeconds < 60 || input.DurationSeconds > 28800 || input.MaxUses < 1 || input.MaxUses > 100)
}

// grantMatches checks immutable deployment scope and remaining capacity before reserving a use.
func (c *Controller) grantMatches(grant *Authorization, actor, host, app string) bool {
	return c.authorizationLive(grant) && grant.Device == actor && grant.Host == host && grant.Uses < grant.MaxUses && c.grantCanDeploy(grant, app)
}

// statelessPlan excludes provisioning or replacing data-bearing workloads from deployment windows.
func statelessPlan(p Plan) bool {
	return p.ID != "" && p.Kind == "" && len(p.Workflow) == 0 && len(p.Dependencies) == 0 && !model.HasVolumes(p.Spec) && !p.StatefulSource
}
