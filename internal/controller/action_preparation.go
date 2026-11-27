package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"hostlier/internal/model"
)

// actionPlanInput checks resolved rollback inputs before any secret or package preparation.
func (c *Controller) actionPlanInput(r *http.Request, g *Authorization, remote bool) bool {
	data, err := deviceRequestBody(r)
	var input deploymentPlanRequest
	if err != nil || json.Unmarshal(data, &input) != nil || !c.grantProjectSwarm(g, input.Spec, remote) {
		return false
	}
	if input.Action != "" && input.Action != "deploy" && input.Action != "rollback" && input.Action != "scale" {
		return false
	}
	// Check the basic capability before making even read-only calls to a member.
	if !hasActions(g, deploymentActions(input.Action, input.Spec, nil)) {
		return false
	}
	prior := c.state.Applications[input.Spec.Name]
	if remote {
		prior, err = c.actionRemoteApp(r, g, input.Spec.Name)
		if err != nil {
			return false
		}
	}
	if prior != nil && !grantSwarmApp(g, prior.SwarmID, input.Spec.Name) {
		return false
	}
	spec := input.Spec
	if input.Action == "rollback" {
		found := false
		if prior != nil {
			for _, revision := range prior.Revisions {
				if revision.ID == input.Revision {
					spec, found = revision.Spec, true
					break
				}
			}
		}
		if !found {
			return false
		}
	}
	if prior != nil && (model.HasVolumes(prior.Spec) || len(prior.ResourceRefs) != 0 || prior.Spec.Workflow != nil) {
		return false
	}
	return c.authorizationLive(g) && g.Uses < g.MaxUses && hasActions(g, deploymentActions(input.Action, spec, prior)) && scopedProject(spec, g.Secrets)
}

// actionRemoteApp binds preparation to the member generation inspected before resolving its app.
func (c *Controller) actionRemoteApp(r *http.Request, g *Authorization, name string) (*Application, error) {
	intent := humanRequest{Path: r.URL.Path}
	if err := c.bindHostIntent(&intent, g.Device); err != nil {
		return nil, err
	}
	if intent.Target.Fingerprint != g.HostFingerprint {
		return nil, model.Err("SCOPE_DENIED", "Host identity changed")
	}
	host := c.state.Hosts[g.Host]
	result, err := c.fleetRequest(r.Context(), host, "GET", "/v1/apps/"+url.PathEscape(name), nil, g.Device, intent.Target)
	var failure *model.Error
	if err != nil && (!errors.As(err, &failure) || failure.Code != "NOT_FOUND") {
		return nil, err
	}
	// The same generation must still hold when the forwarded preparation starts.
	*r = *r.WithContext(context.WithValue(r.Context(), fleetContext{}, intent.Target))
	if err != nil {
		return nil, nil
	}
	data, err := json.Marshal(result)
	var app Application
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

// actionRemoteReceipt checks actor ownership before admitting a member receipt or watch.
func (c *Controller) actionRemoteReceipt(r *http.Request, g *Authorization, path string) bool {
	result, err := c.fleetRequest(r.Context(), c.state.Hosts[g.Host], "GET", path, nil, g.Device, nil)
	if err != nil {
		return false
	}
	data, err := json.Marshal(result)
	var receipt Operation
	if err != nil || json.Unmarshal(data, &receipt) != nil {
		return false
	}
	expected := "controller:" + strings.TrimPrefix(c.store.Authenticate("fleet-actor|"+g.Device), "hmac:")
	if !grantSwarmMatches(g, receipt.SwarmID) {
		return false
	}
	if receipt.Name != "" {
		app, err := c.actionRemoteApp(r, g, receipt.Name)
		if err != nil || app != nil && !grantSwarmMatches(g, app.SwarmID) {
			return false
		}
	}
	return c.authorizationLive(g) && c.state.Hosts[g.Host].Fingerprint == g.HostFingerprint && receipt.Actor == expected
}
