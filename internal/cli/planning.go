package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// planningReceipt carries preparation status without conferring deployment approval.
type planningReceipt struct {
	ID     string         `json:"id"`
	Status string         `json:"status"`
	Plan   map[string]any `json:"plan"`
	Error  *model.Error   `json:"error"`
}

// prepareReviewedPlan uses durable preparation for workflows and preserves legacy routes.
func (s *session) prepareReviewedPlan(flow planFlow, plan *map[string]any) error {
	// Nonworkflow plans retain their existing synchronous protocol.
	if !asynchronousPlan(flow) {
		return s.request("POST", flow.path, flow.body, plan)
	}
	receipt, err := s.acceptPlanning(flow)
	// Older controllers explicitly lacking the endpoint retain compatibility.
	if planningErrorCode(err) == "NOT_FOUND" {
		return s.request("POST", flow.path, flow.body, plan)
	}
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return err
	}
	fmt.Fprintln(s.errOut, "Planning job "+receipt.ID+": preparing review; no deployment approved.")
	return s.pollPlanning(receipt, plan)
}

// asynchronousPlan selects workflows independent of provider package naming.
func asynchronousPlan(flow planFlow) bool {
	// Keep private preparation within its bounded validated contract.
	if flow.path != "/v1/plans" {
		return false
	}
	raw, err := json.Marshal(flow.body)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return false
	}
	var body struct {
		Spec model.Project `json:"spec"`
	}
	// Keep private preparation within its bounded validated contract.
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	return body.Spec.Workflow != nil
}

// acceptPlanning retries lost acceptance replies with exactly the same request identity.
func (s *session) acceptPlanning(flow planFlow) (planningReceipt, error) {
	body := map[string]any{"requestId": client.RequestID(), "request": flow.body}
	var receipt planningReceipt
	// Check each declared identity against the explicit contract.
	for {
		err := s.request("POST", "/v1/planning", body, &receipt)
		// Only connection failures permit replay of the immutable accepted request.
		if planningErrorCode(err) != "CONNECTION" {
			return receipt, err
		}
		// Stop before publishing an incomplete or invalid preparation.
		if err := s.pause(); err != nil {
			return receipt, err
		}
	}
}

// pollPlanning reconnects observation without executing or approving deployment work.
func (s *session) pollPlanning(receipt planningReceipt, plan *map[string]any) error {
	// Check each declared identity against the explicit contract.
	for {
		// Completed preparation still requires the ordinary exact fingerprint approval.
		if receipt.Status == "completed" {
			// Keep private preparation within its bounded validated contract.
			if receipt.Plan == nil {
				return model.Err("INVALID_RESPONSE", "Planning returned no reviewed plan")
			}
			*plan = receipt.Plan
			return nil
		}
		// Keep private preparation within its bounded validated contract.
		if receipt.Error != nil {
			return receipt.Error
		}
		// Interrupted jobs are never silently reissued after controller restart.
		if receipt.Status != "queued" && receipt.Status != "running" {
			return model.Err("RECOVERY_REQUIRED", "Planning interrupted; request a new plan")
		}
		// Stop before publishing an incomplete or invalid preparation.
		if err := s.pause(); err != nil {
			return err
		}
		err := s.request("GET", "/v1/planning/"+url.PathEscape(receipt.ID), nil, &receipt)
		// A disconnected observation retries the same durable job identity.
		if err != nil && planningErrorCode(err) != "CONNECTION" {
			return err
		}
	}
}

// planningErrorCode classifies safe controller errors without inspecting transport diagnostics.
func planningErrorCode(err error) string {
	var failure *model.Error
	// Stop before publishing an incomplete or invalid preparation.
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}
