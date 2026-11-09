package cli

import (
	"net/url"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// deploy prepares a deployment or rollback for the shared approval workflow.
func (s *session) deploy() error {
	minion, cleanup, err := s.prepareMinionDeployment()
	defer cleanup()
	// Failed builds or consent must never reach artifact upload or deployment planning.
	if err != nil {
		return err
	}
	spec, revision, err := s.deploymentSpecFor(minion)
	// Stop planning when deployment inputs or their route cannot be resolved.
	if err != nil {
		return err
	}
	return s.reviewDeployment(spec, revision)
}

// reviewDeployment submits the immutable deployment to the same controller review for all sources.
func (s *session) reviewDeployment(spec model.Project, revision string) error {
	spec, source, err := s.deploymentRoute(spec)
	// Stop planning when deployment inputs or their route cannot be resolved.
	if err != nil {
		return err
	}
	// Local secret declarations are synchronized only after the final project and route validate.
	if err = s.uploadProjectSecrets(); err != nil {
		return err
	}
	body := map[string]any{"spec": spec}
	// Include the retained revision identity in rollback planning.
	if s.command == "rollback" {
		body["action"] = "rollback"
		body["revision"] = revision
	}
	return s.reviewPlan(planFlow{path: "/v1/plans", body: body, routeSource: source, readOnly: s.command == "plan", prompt: "Approve this exact deployment plan? [y/N] ", staleMessage: "Server state changed. Review and approve the updated plan."})
}

// deploymentSpec loads project inputs or the existing application required for rollback.
func (s *session) deploymentSpecFor(minion *minionDeployment) (model.Project, string, error) {
	// Read the local project for ordinary deploy and plan commands.
	if s.command != "rollback" {
		p, err := s.readMinionProject(minion)
		// Invalid input cannot trigger artifact preparation.
		if err != nil {
			return p, "", err
		}
		p, err = s.prepareDeploymentProject(p)
		return p, "", err
	}
	// Require an application; an omitted revision selects its immediate predecessor.
	if s.sub == "" {
		return model.Project{}, "", model.Err("ARGUMENTS", "Usage: rollback APP [--revision REVISION]")
	}
	var app rollbackApplication
	// Stop before selecting a revision if application history cannot be read.
	if err := s.request("GET", "/v1/apps/"+url.PathEscape(s.sub), nil, &app); err != nil {
		return model.Project{}, "", err
	}
	revision := s.flags["revision"]
	// Explicit IDs retain their existing behavior; otherwise resolve one predecessor.
	if revision == "" {
		var err error
		revision, err = app.previousRevision()
		// Missing history cannot produce an implicit rollback plan.
		if err != nil {
			return model.Project{}, "", err
		}
	}
	return app.Spec, revision, nil
}

// prepareDeploymentProject validates before preparing and uploading any local sources.
func (s *session) prepareDeploymentProject(p model.Project) (model.Project, error) {
	// Snapshot config sources before validating the immutable deployment input.
	p, err := s.prepareConfigFiles(p)
	// Every declared file must be available before network preparation.
	if err != nil {
		return p, err
	}
	// Validate the complete topology before preparing or uploading local artifacts.
	if err = config.ValidateInput(p); err != nil {
		return p, err
	}
	p, err = s.prepareArtifacts(p)
	// Preparation produces the immutable source consumed by ordinary validation.
	if err != nil {
		return p, err
	}
	p, err = config.ValidateProject(p)
	return p, err
}

// rollbackApplication reads history in the controller's oldest-to-newest order.
type rollbackApplication struct {
	applicationSpec
	Revision  string `json:"revision"`
	Revisions []struct {
		ID string `json:"id"`
	} `json:"revisions"`
}

// previousRevision pins the predecessor of the active revision before plan review.
func (app rollbackApplication) previousRevision() (string, error) {
	// History is ordered oldest to newest by the controller.
	for i := len(app.Revisions) - 1; i >= 0; i-- {
		// Resolve relative to the active revision rather than assuming the latest item.
		if app.Revision != "" && app.Revisions[i].ID == app.Revision {
			// A missing or duplicate predecessor cannot identify a safe target.
			if i > 0 && app.Revisions[i-1].ID != "" && app.Revisions[i-1].ID != app.Revision {
				return app.Revisions[i-1].ID, nil
			}
			break
		}
	}
	return "", model.Err("NOT_FOUND", "No previous revision available; inspect application history with hostlier status APP --json or specify --revision")
}

// applicationSpec decodes the retained application specification used for rollback.
type applicationSpec struct {
	Spec model.Project `json:"spec"`
}

// deploymentRoute records route provenance and validates the final public address.
func (s *session) deploymentRoute(spec model.Project) (model.Project, string, error) {
	source := "project"
	// Private workloads never prompt for public DNS or allocate a route.
	if !model.Public(spec) {
		// A public override cannot silently change a private workload's exposure.
		if s.flags["route"] != "" {
			return spec, source, model.Err("INVALID_CONFIG", "--route requires public exposure")
		}
		return spec, "private", nil
	}
	// Use the explicit route override and report its provenance in the plan.
	if s.flags["route"] != "" {
		spec.Route = s.flags["route"]
		source = "--route"
	}
	// Ask for a full address only when no route was configured.
	if spec.Route == "" {
		var err error
		spec.Route, err = s.ask("Full public address (hostname and optional path): ", false)
		// Honor input cancellation or read failure before consuming the supplied value.
		if err != nil {
			return spec, source, err
		}
		source = "interactive selection"
	}
	// Named secondary routes are explicit; only the selected primary follows --route.
	if len(spec.Endpoints) > 0 {
		e := spec.Endpoints[model.PrimaryEndpointName(spec)]
		e.Route = spec.Route
		spec.Endpoints[model.PrimaryEndpointName(spec)] = e
	}
	// Keep v2 endpoint routing authoritative after overrides or interactive selection.
	if spec.Endpoint != nil {
		spec.Endpoint.Route = spec.Route
	}
	spec, err := config.ValidateProject(spec)
	return spec, source, err
}

// lifecycle validates application identity before starting a lifecycle action.
func (s *session) lifecycle() error {
	// Reject this path: Application name required.
	if s.sub == "" {
		return model.Err("ARGUMENTS", "Application name required")
	}
	return s.performAction("/v1/apps/"+url.PathEscape(s.sub)+"/action", s.command, "Permanently purge this application's owned data? [y/N] ", "--purge requires delete")
}

// performAction requires explicit purge approval and observes the accepted lifecycle operation.
func (s *session) performAction(path, action, prompt, purgeError string) error {
	body := map[string]any{"action": action}
	// Require a separate destructive-data approval before setting purge.
	if s.flags["purge"] != "" {
		// Allow destructive purge only as part of an explicit deletion.
		if action != "delete" {
			return model.Err("ARGUMENTS", purgeError)
		}
		// Require explicit interactive approval before performing the reviewed mutation.
		if err := s.approve(prompt); err != nil {
			return err
		}
		body["purge"] = true
	}
	var result operationReceipt
	// Require the controller to accept this request before advancing the command workflow.
	if err := s.request("POST", path, body, &result); err != nil {
		return err
	}
	return s.observeAccepted(result)
}
