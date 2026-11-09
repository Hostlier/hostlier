package cli

import (
	"hostlier/internal/config"
	"hostlier/internal/model"
	"net/url"
)

// sidecar prepares an ordinary atomic group deployment to attach or detach a member.
func (s *session) sidecar() error {
	// Both commands identify the existing application and its stable component name.
	if s.target == "" || s.flags["name"] == "" || (s.sub != "attach" && s.sub != "detach") {
		return model.Err("ARGUMENTS", "Usage: sidecar attach|detach APP --name NAME [--file MEMBER.yaml]")
	}
	var app applicationSpec
	// Read the current group so unchanged components remain pinned.
	if err := s.request("GET", "/v1/apps/"+url.PathEscape(s.target), nil, &app); err != nil {
		return err
	}
	p := upgradeGroup(app.Spec)
	var err error
	p, err = s.editSidecar(p)
	// Invalid member changes cannot produce a deployment plan.
	if err != nil {
		return err
	}
	p, err = config.ValidateProject(p)
	// Detaching a member referenced by the primary is rejected during planning.
	if err != nil {
		return err
	}
	// Member secret values follow the same protected synchronization path as primary values.
	if err = s.uploadProjectSecrets(); err != nil {
		return err
	}
	return s.reviewPlan(planFlow{path: "/v1/plans", body: map[string]any{"spec": p}, routeSource: "existing application", prompt: "Approve this exact group deployment plan? [y/N] ", staleMessage: "Server state changed. Review and approve the updated group plan."})
}

// upgradeGroup preserves v1 public routing while opting into explicit group semantics.
func upgradeGroup(p model.Project) model.Project {
	// Existing v2 declarations already carry their exposure and process contract.
	if p.Version == 1 {
		p.Version = 2
		p.Source = &model.Source{Image: p.Image}
		p.Workload = "service"
		p.Endpoint = &model.Endpoint{Port: p.Port, Exposure: "public", Route: p.Route}
	}
	// Decode produces a fresh map, so editing cannot mutate retained controller state.
	if p.Sidecars == nil {
		p.Sidecars = map[string]model.Project{}
	}
	return p
}

// editSidecar changes exactly one component before full-group validation and approval.
func (s *session) editSidecar(p model.Project) (model.Project, error) {
	name := s.flags["name"]
	_, exists := p.Sidecars[name]
	// Detach never removes an implicit or misspelled component.
	if s.sub == "detach" {
		// Missing targets must not silently turn into unrelated redeployments.
		if !exists {
			return p, model.Err("NOT_FOUND", "Sidecar not found")
		}
		delete(p.Sidecars, name)
		return p, nil
	}
	// Attach is explicit; updates use the full desired project to avoid accidental replacement.
	if exists {
		return p, model.Err("ALREADY_EXISTS", "Sidecar already exists; update the deployment to replace it")
	}
	member, err := s.readProjectInput(s.target + "/" + name)
	// Malformed local input must not trigger artifact preparation.
	if err != nil {
		return p, err
	}
	member.Name, member.Version = name, 2
	p.Sidecars[name] = member
	// Validate the complete desired group before local code packaging or upload.
	if err = config.ValidateInput(p); err != nil {
		return p, err
	}
	member, err = s.prepareArtifacts(member)
	// Member artifacts use the same authenticated, immutable preparation path.
	if err != nil {
		return p, err
	}
	member.Name, member.Version = name, 2
	p.Sidecars[name] = member
	return p, nil
}
