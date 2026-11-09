package cli

import (
	"errors"
	"net/url"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// planFlow supplies command-specific content to the common preview-and-approval workflow.
type planFlow struct {
	// refresh rebuilds intent from current desired state before every reviewed attempt.
	refresh                           func() (map[string]any, error)
	path                              string
	body                              map[string]any
	routeSource, prompt, staleMessage string
	readOnly                          bool
}

// operationReceipt identifies durable accepted work independently of the CLI process.
type operationReceipt struct {
	OperationID string `json:"operationId"`
}

// reviewPlan refreshes stale plans and requires fresh approval before observing accepted work.
func (s *session) reviewPlan(flow planFlow) error {
	// Allow at most three freshly reviewed attempts when concurrent state changes invalidate plans.
	for attempt := 0; attempt < 3; attempt++ {
		receipt, err := s.reviewPlanAttempt(flow)
		var failure *model.Error
		// Refresh stale state and ask for fresh approval before another apply attempt.
		if errors.As(err, &failure) && failure.Code == "STALE_PLAN" && attempt < 2 {
			s.message(flow.staleMessage)
			continue
		}
		// Stop the approval workflow when the current attempt fails.
		if err != nil {
			return err
		}
		// Stop after printing a read-only plan.
		if flow.readOnly {
			return nil
		}
		return s.observeAccepted(receipt)
	}
	return model.Err("STALE_PLAN", "Server state changed; review a new plan")
}

// reviewPlanAttempt prints the exact plan and requires approval before applying its fingerprint.
func (s *session) reviewPlanAttempt(flow planFlow) (operationReceipt, error) {
	var accepted operationReceipt
	// Rebase patch-style intent on the latest desired specification before reviewing a new plan.
	if flow.refresh != nil {
		var err error
		flow.body, err = flow.refresh()
		// A failed read cannot authorize a plan based on obsolete omitted values.
		if err != nil {
			return accepted, err
		}
	}
	var plan map[string]any
	// Require the controller to accept this request before advancing the command workflow.
	if err := s.prepareReviewedPlan(flow, &plan); err != nil {
		return accepted, err
	}
	display := map[string]any{}
	// Copy plan fields before adding CLI-only display metadata.
	for key, value := range plan {
		display[key] = value
	}
	display["targetServer"] = s.serverTarget
	// Include route provenance only for deployment planning.
	if flow.routeSource != "" {
		display["routeSource"] = flow.routeSource
	}
	// Stop if the reviewed plan or receipt cannot be shown to the caller.
	if err := s.printReview(display); err != nil {
		return accepted, err
	}
	// Leave runtime state untouched after printing a read-only plan.
	if flow.readOnly {
		return accepted, nil
	}
	// Require explicit interactive approval before performing the reviewed mutation.
	if err := s.approve(flow.prompt); err != nil {
		return accepted, err
	}
	id, _ := plan["id"].(string)
	err := s.request("POST", "/v1/plans/"+url.PathEscape(id)+"/apply", map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": client.RequestID()}, &accepted)
	return accepted, err
}

// observeAccepted prints the durable receipt and follows the operation unless detached.
func (s *session) observeAccepted(receipt operationReceipt) error {
	// Stop if the reviewed plan or receipt cannot be shown to the caller.
	if err := s.print(receipt); err != nil {
		return err
	}
	return s.waitForOperation(receipt.OperationID)
}
