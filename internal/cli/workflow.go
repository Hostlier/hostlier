package cli

import (
	"hostlier/internal/model"
	"net/url"
)

// workflow reviews retained uncertain steps and authorizes explicit metadata reconciliation.
func (s *session) workflow() error {
	path := "/v1/workflows/" + url.PathEscape(s.target)
	// Listing exposes only safe retained step identities.
	if s.sub == "list" && len(s.parsed.args) == 3 {
		return s.getPrint(path)
	}
	// Inspection and reconciliation both name exactly one application step.
	if len(s.parsed.args) != 4 || (s.sub != "inspect" && s.sub != "reconcile") {
		return model.Err("ARGUMENTS", "Usage: workflow list APP | workflow inspect APP STEP | workflow reconcile APP STEP")
	}
	path += "/" + url.PathEscape(s.position(3))
	var review map[string]any
	// Read authoritative evidence before displaying or approving recovery.
	if err := s.request("GET", path, nil, &review); err != nil {
		return err
	}
	// A failed preview write cannot authorize any mutation.
	if err := s.print(review); err != nil {
		return err
	}
	// Read-only inspection needs no approval.
	if s.sub == "inspect" {
		return nil
	}
	return s.reconcileWorkflow(path, review)
}

// reconcileWorkflow approves the exact observed recovery before requesting a state change.
func (s *session) reconcileWorkflow(path string, review map[string]any) error {
	// Uncertain outcomes cannot be made successful through user assertion alone.
	if review["status"] == "uncertain" {
		return model.Err("WORKFLOW_UNCERTAIN", "Outcome remains uncertain")
	}
	// A new explicit approval covers recovery metadata, not replaying external work.
	if err := s.approve("Apply this verified workflow recovery? [y/N] "); err != nil {
		return err
	}
	var result any
	// The server rechecks package evidence before committing this fingerprint.
	if err := s.request("POST", path, map[string]any{"approved": true, "fingerprint": review["fingerprint"]}, &result); err != nil {
		return err
	}
	return s.print(result)
}

// usage reads current runtime measurements independently of deployment mutation.
func (s *session) usage() error {
	// Require a single application rather than guessing a deployment target.
	if len(s.parsed.args) != 2 {
		return model.Err("ARGUMENTS", "Usage: usage APP")
	}
	return s.getPrint("/v1/apps/" + url.PathEscape(s.sub) + "/usage")
}
