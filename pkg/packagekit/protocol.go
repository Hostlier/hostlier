package packagekit

import (
	"bytes"
	"encoding/json"
	"errors"
	"hostlier/pkg/workflow"
	"io"
)

// decode rejects unsupported protocol versions, actions and extra request documents.
func decode(data []byte) (workflow.Request, error) {
	var req workflow.Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	// Exact decoding prevents trailing commands and silent schema mismatches.
	if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF {
		return req, errors.New("invalid package request")
	}
	// The key binds effects to one controller-selected operation identity.
	if req.Version != 1 || req.Key == "" {
		return req, errors.New("invalid package protocol")
	}
	// Only these lifecycle actions belong to protocol version one.
	switch req.Action {
	// Version one negotiates these explicit lifecycle actions.
	case "plan", "apply", "inspect", "reconcile":
		return req, nil
	}
	return req, errors.New("unsupported package action")
}

// validResponse prevents output publication before completed authoritative inspection.
func validResponse(action string, response workflow.Response) bool {
	// Every protocol response belongs to the negotiated schema.
	if response.Version != 1 {
		return false
	}
	// Actions have mutually exclusive success shapes.
	switch action {
	// Planning publishes protected review state only.
	case "plan":
		return validPlanResponse(response)
	// Application publishes the complete private execution result.
	case "apply":
		return validApplyResponse(response)
	// Observation publishes only authoritative lifecycle evidence.
	case "inspect", "reconcile":
		return validInspectResponse(response)
	}
	return false
}

// validPlanResponse accepts only protected review state before any provisioning result.
func validPlanResponse(response workflow.Response) bool {
	return response.Prepared != nil && response.Result == nil && response.Inspection == nil
}

// validApplyResponse accepts complete private state without mixing lifecycle response kinds.
func validApplyResponse(response workflow.Response) bool {
	return response.Result != nil && response.Prepared == nil && response.Inspection == nil && workflow.ValidResult(*response.Result)
}

// validInspectResponse enforces authoritative status and private result validation together.
func validInspectResponse(response workflow.Response) bool {
	return response.Inspection != nil && response.Inspection.Valid() && response.Prepared == nil && response.Result == nil
}
