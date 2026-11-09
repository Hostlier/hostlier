package controller

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// sandboxPrefixRE keeps prefixes inside the application-name grammar and forces a separator.
var sandboxPrefixRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}-$`)

// validateSandboxRequest bounds name prefix, concurrent apps and optional route domain.
func validateSandboxRequest(input authorizationRequest) error {
	// Exact-name windows keep their existing contract.
	if input.AppPrefix == "" {
		return sandboxFieldsAbsent(input)
	}
	// Ownership and teardown use the controller's own app records.
	if input.Host != "local" {
		return model.Err("INVALID_AUTHORIZATION", "Sandbox windows currently support --on local only")
	}
	// A prefix must end in a hyphen so try- cannot match tryout.
	if !sandboxPrefixRE.MatchString(input.AppPrefix) {
		return model.Err("INVALID_AUTHORIZATION", "App prefix must be lowercase letters, digits and hyphens, ending with a hyphen (for example try-)")
	}
	// Concurrent capacity is always explicit and bounded.
	if input.MaxApps < 1 || input.MaxApps > 20 {
		return model.Err("INVALID_AUTHORIZATION", "Sandbox windows require 1–20 maximum apps")
	}
	// An optional route domain must be a concrete DNS name.
	if input.RouteDomain != "" {
		// Reject wildcards, schemes and paths before recording the boundary.
		if _, err := config.NormalizeDomain(input.RouteDomain); err != nil {
			return model.Err("INVALID_AUTHORIZATION", "Route domain must be a DNS name such as try.apps.example.com")
		}
	}
	return nil
}

// sandboxFieldsAbsent rejects sandbox-only options on exact-name windows.
func sandboxFieldsAbsent(input authorizationRequest) error {
	// These options only have meaning together with an app prefix.
	if input.MaxApps != 0 || input.RouteDomain != "" || input.KeepApps {
		return model.Err("INVALID_AUTHORIZATION", "Max apps, route domain and keep apps require an app prefix")
	}
	return nil
}

// sandboxGrant reports whether a window delegates a name prefix rather than exact apps.
func sandboxGrant(g *Authorization) bool {
	return g != nil && g.AppPrefix != ""
}

// sandboxOwned reports whether this grant created the named app.
func sandboxOwned(g *Authorization, name string) bool {
	return sandboxGrant(g) && containsName(g.SandboxApps, name)
}

// grantCoversApp admits reads and lifecycle checks for exact names or owned sandbox apps.
func grantCoversApp(g *Authorization, name string) bool {
	return containsName(g.Apps, name) || sandboxOwned(g, name)
}

// grantCanDeploy admits exact names, or prefixed names that are new or owned by this device's sandbox.
func (c *Controller) grantCanDeploy(g *Authorization, name string) bool {
	// Exact-name windows keep their original matching.
	if containsName(g.Apps, name) {
		return true
	}
	// Prefix authority never extends beyond its declared namespace.
	if !sandboxGrant(g) || !strings.HasPrefix(name, g.AppPrefix) {
		return false
	}
	// An existing or in-flight app may only be updated by the sandbox that created it.
	if c.state.Applications[name] != nil || c.busy(name) {
		return c.sandboxOwner(g.Device, name) != nil
	}
	return c.sandboxCount(g) < g.MaxApps
}

// sandboxOwner finds the device's sandbox grant that created an app, live or ended.
func (c *Controller) sandboxOwner(device, name string) *Authorization {
	// Ownership survives window renewal for the same device only.
	for _, g := range c.state.Authorizations {
		// Another device's sandbox cannot be adopted.
		if g.Device == device && sandboxOwned(g, name) {
			return g
		}
	}
	return nil
}

// sandboxCount counts this window's apps that still exist or have pending work.
func (c *Controller) sandboxCount(g *Authorization) int {
	count := 0
	// Deleted apps free capacity once their removal has finished.
	for _, name := range g.SandboxApps {
		// Count active records and queued first deployments alike.
		if c.sandboxAppActive(name) {
			count++
		}
	}
	return count
}

// sandboxAppActive treats retained deleted records as inactive; deletion keeps rollback history.
func (c *Controller) sandboxAppActive(name string) bool {
	app := c.state.Applications[name]
	return app != nil && app.Status != "deleted" || c.busy(name)
}

// claimSandboxApp records creation, moving ownership from an earlier window of the same device.
func (c *Controller) claimSandboxApp(g *Authorization, name string) {
	// Exact-name deployments are not sandbox-owned.
	if !sandboxGrant(g) || containsName(g.Apps, name) || sandboxOwned(g, name) {
		return
	}
	// One owner keeps teardown unambiguous.
	if prior := c.sandboxOwner(g.Device, name); prior != nil {
		prior.SandboxApps = withoutName(prior.SandboxApps, name)
	}
	g.SandboxApps = append(g.SandboxApps, name)
}

// withoutName returns a copy without one exact name.
func withoutName(values []string, name string) []string {
	out := []string{}
	// Preserve order for readable authorization listings.
	for _, value := range values {
		// Drop only the exact matching identity.
		if value != name {
			out = append(out, value)
		}
	}
	return out
}

// grantSecrets lists secret names a window may expose to one app.
func grantSecrets(g *Authorization, _ string) []string {
	return g.Secrets
}

// sandboxRoutesAllowed keeps every public route of a sandbox app inside its route domain.
func sandboxRoutesAllowed(g *Authorization, spec model.Project) bool {
	// No route boundary applies to exact windows or unbounded sandboxes.
	if !sandboxGrant(g) || g.RouteDomain == "" {
		return true
	}
	routes := []string{spec.Route}
	// Named endpoints can each publish a route.
	for _, endpoint := range spec.Endpoints {
		routes = append(routes, endpoint.Route)
	}
	// Every declared route must parse inside the delegated domain.
	for _, route := range routes {
		// Private services and workers declare no route.
		if route == "" {
			continue
		}
		// ParseRoute applies the same subdomain matching as controller domains.
		if _, err := config.ParseRoute(route, []string{g.RouteDomain}, ""); err != nil {
			return false
		}
	}
	return true
}

// applySandboxAction approves lifecycle actions on apps created by a live sandbox of the same device.
func (c *Controller) applySandboxAction(a *HumanApproval, request humanRequest) bool {
	host, name, ok := sandboxActionTarget(request)
	// Only non-purging application actions are delegable.
	if !ok {
		return false
	}
	// The first matching live sandbox supplies provenance.
	for _, g := range c.state.Authorizations {
		// Ownership, device, host and liveness must all hold.
		if g.Device != a.Actor || g.Host != host || !sandboxOwned(g, name) || !c.authorizationLive(g) {
			continue
		}
		// Remote hosts remain pinned to the enrolled identity.
		if request.Target != nil && g.HostFingerprint != request.Target.Fingerprint {
			continue
		}
		c.approveWithGrant(a, g)
		c.log("Sandbox app action authorized by bounded grant: "+g.ID, "", "info")
		return true
	}
	return false
}

// sandboxActionTarget extracts host and app from a non-purging lifecycle request.
func sandboxActionTarget(request humanRequest) (string, string, bool) {
	host, inner, isHost := model.FleetPath(request.Path)
	// Requests without a host prefix target the controller's own runtime.
	if !isHost {
		host, inner = "local", request.Path
	}
	parts := strings.Split(strings.Trim(inner, "/"), "/")
	// Only POST /v1/apps/NAME/action is a delegable lifecycle route.
	if request.Method != "POST" || len(parts) != 4 || parts[1] != "apps" || parts[3] != "action" {
		return "", "", false
	}
	var body actionRequest
	// Purge is never delegated; data removal stays with the owner.
	if json.Unmarshal(request.Body, &body) != nil || !validAction(body) || body.Purge {
		return "", "", false
	}
	return host, parts[2], true
}

// startSandboxMonitor removes apps owned by sandbox windows once they end.
func (c *Controller) startSandboxMonitor() {
	c.wg.Add(1)
	go func() { // This worker exits with the controller.
		defer c.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		// Check ended windows once per minute.
		for {
			// Stop with the controller; otherwise sweep ended windows.
			select {
			case <-c.ctx.Done(): // Shutdown stops further teardown.
				return
			case <-ticker.C: // Teardown queues ordinary delete operations.
				c.mu.Lock()
				c.teardownSandboxes()
				c.mu.Unlock()
			}
		}
	}()
}

// teardownSandboxes queues deletion for every ended window that has not kept its apps.
func (c *Controller) teardownSandboxes() {
	// Revoked, expired and invalidated windows all end the sandbox.
	for _, g := range c.state.Authorizations {
		// Live, kept or completed sandboxes need no action.
		if !sandboxGrant(g) || g.TornDown || g.KeepApps || c.authorizationLive(g) {
			continue
		}
		// Mark completion only after every owned app has been queued or is gone.
		if c.teardownSandbox(g) {
			g.TornDown = true
			c.state.Generation++
			c.store.Save(c.state)
			c.log("Sandbox window ended; apps queued for deletion: "+g.ID, "", "info")
		}
	}
}

// teardownSandbox queues one non-purging delete per owned app and reports whether all were handled.
func (c *Controller) teardownSandbox(g *Authorization) bool {
	done := true
	// Retained data is never purged by teardown.
	for _, name := range g.SandboxApps {
		app := c.state.Applications[name]
		// In-flight work, including a first deployment with no record yet, is retried next sweep.
		if c.busy(name) {
			done = false
			continue
		}
		// Removed or already deleted apps need no work.
		if app == nil || app.Status == "deleted" {
			continue
		}
		// A rejected delete is retried on the next sweep.
		if _, err := c.queueAppAction(app, "delete", false, "sandbox:"+g.ID); err != nil {
			done = false
		}
	}
	return done
}
