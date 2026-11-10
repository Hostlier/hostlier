package controller

import (
	"encoding/json"
	"hostlier/internal/model"
	"net/http"
	"regexp"
	"strings"
)

var scopeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// liveApprovalKey unifies passkey, optional hardware-key and explicit local administrator authority.
func (c *Controller) liveApprovalKey(id string) bool {
	// Proceed only for this explicitly validated protocol state.
	if id == "administrator:"+c.state.HumanRealm {
		return true
	}
	// Proceed only for this explicitly validated protocol state.
	if c.state.Human == nil {
		return false
	}
	// Proceed only for this explicitly validated protocol state.
	if key, ok := c.state.Human.Passkeys[id]; ok {
		return !key.Revoked
	}
	key, ok := c.state.Human.Keys[id]
	return ok && !key.Revoked
}

// scopedClient identifies device credentials that must never fall through to general owner approval.
func (c *Controller) scopedClient(actor string) bool {
	v := c.state.Clients[actor]
	return v != nil && v.DeploymentGrant != ""
}

// validateGrantExecution rechecks issuance, revocation, expiry and credential binding immediately before execution.
func (c *Controller) validateGrantExecution(a *HumanApproval) error {
	g := c.state.Authorizations[a.AuthorizationID]
	// Reject the request: CLI credential expired, revoked or changed.
	if !c.authorizationLive(g) || g.Device != a.Actor || g.KeyFingerprint != a.CredentialID {
		return model.Err("HUMAN_VERIFICATION", "CLI credential expired, revoked or changed")
	}
	// Recheck absence at execution so an approved creation cannot become an overwrite.
	if g.Scope != "all" && strings.HasPrefix(a.Path, "/v1/secrets/") && !c.secretCreationAllowed(g, a) {
		return model.Err("SCOPE_DENIED", "Secret creation permission does not allow overwriting an existing secret")
	}
	return nil
}

// checkScopedRoute prevents a deploy credential from reading other apps or reaching owner administration.
func (c *Controller) checkScopedRoute(r *http.Request, device *Client) error {
	// Proceed only for this explicitly validated protocol state.
	if device.DeploymentGrant == "" {
		return nil
	}
	grant := c.state.Authorizations[device.DeploymentGrant]
	// Reject the request: CLI credential expired or revoked.
	if !c.authorizationLive(grant) {
		return model.Err("UNAUTHORIZED", "CLI credential expired or revoked")
	}
	// Proceed only for this explicitly validated protocol state.
	if grant.Scope == "all" || c.scopedRouteAllowed(r, grant) || c.sandboxRouteAllowed(r, device.ID) {
		return nil
	}
	return model.Err("SCOPE_DENIED", "Request is outside this deployment credential's scope")
}

// sandboxRouteAllowed lets an owner-approved live sandbox window for the same device widen its routes.
func (c *Controller) sandboxRouteAllowed(r *http.Request, device string) bool {
	// Only windows granted to this exact device count.
	for _, g := range c.state.Authorizations {
		// Ended windows and exact-name windows add nothing beyond the credential's own grant.
		if g.Device == device && sandboxGrant(g) && c.authorizationLive(g) && c.scopedRouteAllowed(r, g) {
			return true
		}
	}
	return false
}

// scopedRouteAllowed admits only app-bound preparation, execution receipts and read-only status.
func (c *Controller) scopedRouteAllowed(r *http.Request, g *Authorization) bool {
	// The collection routes are the only admitted writes before exact-plan approval.
	switch r.Method + " " + r.URL.Path {
	// Handle only this explicitly admitted route.
	case "GET /v1/authorizations/self", "GET /v1/packages/platform":
		return true
	// Handle only this explicitly admitted route.
	case "POST /v1/plans":
		return c.scopedPlanInput(r, g)
	// Handle only this explicitly admitted route.
	case "POST /v1/approvals":
		return true
	}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Reject malformed or unrelated API namespaces.
	if len(p) < 3 || p[0] != "v1" {
		return false
	}
	// Reads and execution must belong to the bound actor or exact named app.
	switch p[1] {
	// Handle only this explicitly admitted route.
	case "approvals":
		return c.scopedApprovalRoute(r, p, g)
	// Handle only this explicitly admitted route.
	case "apps":
		return scopedAppRoute(r, p, g)
	// Handle only this explicitly admitted route.
	case "operations":
		return c.scopedOperationRoute(r, p, g)
	// Handle only this explicitly admitted route.
	case "plans":
		return c.scopedPlanRead(r, p, g)
	}
	return scopedArtifactRoute(r, p)
}

// scopedArtifactRoute admits bounded staging, receipt status and verified publication, never archive reads or deletion.
func scopedArtifactRoute(r *http.Request, p []string) bool {
	return p[1] == "artifacts" && (r.Method == "GET" && len(p) == 3 && p[2] == "platform" || r.Method == "POST" && (len(p) == 3 && (p[2] == "commit" || p[2] == "status" || p[2] == "pull") || len(p) == 4 && p[3] == "chunks"))
}

// scopedApprovalRoute permits execution only of the original device's retained request.
func (c *Controller) scopedApprovalRoute(r *http.Request, p []string, g *Authorization) bool {
	a := c.state.HumanApprovals[p[2]]
	// Another device's receipt cannot authorize this credential.
	if a == nil || a.Actor != g.Device {
		return false
	}
	return r.Method == "GET" && len(p) == 3 || r.Method == "POST" && len(p) == 4 && p[3] == "execute"
}

// scopedAppRoute restricts both status and logs to exact app names or apps the sandbox created.
func scopedAppRoute(r *http.Request, p []string, g *Authorization) bool {
	return r.Method == "GET" && grantCoversApp(g, p[2]) && (len(p) == 3 || len(p) == 4 && p[3] == "logs")
}

// scopedOperationRoute keeps progress streams inside the originating device's operations.
func (c *Controller) scopedOperationRoute(r *http.Request, p []string, g *Authorization) bool {
	o := c.state.Operations[p[2]]
	return r.Method == "GET" && o != nil && o.Actor == g.Device && (len(p) == 3 || len(p) == 4 && p[3] == "events")
}

// scopedPlanInput rejects unsupported preparation before secret resolution or package execution.
func (c *Controller) scopedPlanInput(r *http.Request, g *Authorization) bool {
	body, err := deviceRequestBody(r)
	// Preserve the authenticated body for the ordinary strict API decoder.
	if err != nil {
		return false
	}
	var input deploymentPlanRequest
	// Scoped credentials deploy explicit stateless projects; owner flows handle rollback and scaling.
	if json.Unmarshal(body, &input) != nil || (input.Action != "" && input.Action != "deploy") || !c.grantCanDeploy(g, input.Spec.Name) {
		return false
	}
	return scopedProject(input.Spec, grantSecrets(g, input.Spec.Name)) && sandboxRoutesAllowed(g, input.Spec) && !c.statefulSource(input.Spec.Name)
}

// scopedProject checks nested secret references and forbids preparation with data-bearing effects.
func scopedProject(p model.Project, allowed []string) bool {
	// Workflows and dependencies may execute external provisioning during preparation.
	if p.Workflow != nil || len(p.Dependencies) > 0 || len(p.Volumes) > 0 {
		return false
	}
	// Native Fused bindings are checked before they are lowered to environment references.
	if p.Fused != nil && !containsName(allowed, p.Fused.TokenSecret) {
		return false
	}
	// Every environment-secret reference must be named in the owner's explicit grant.
	for _, name := range p.Secrets {
		// Proceed only for this explicitly validated protocol state.
		if !containsName(allowed, name) {
			return false
		}
	}
	// Sidecars inherit the same limits rather than creating a second authority boundary.
	for _, member := range p.Sidecars {
		// Proceed only for this explicitly validated protocol state.
		if !scopedProject(member, allowed) {
			return false
		}
	}
	return true
}

// scopedPlanRead lets a device recover only its own immutable prepared plan.
func (c *Controller) scopedPlanRead(r *http.Request, p []string, g *Authorization) bool {
	plan := c.state.Plans[p[2]]
	return r.Method == "GET" && len(p) == 3 && plan != nil && plan.Actor == g.Device
}
