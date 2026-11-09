package controller

import "hostlier/internal/model"

// operationInput is the immutable execution snapshot captured under the state lock.
type operationInput struct {
	operation *Operation
	previous  *model.Deployment
	secrets   map[string]string
	err       error
}

// operationResult carries engine output back to the durable-state update phase.
type operationResult struct {
	deployed     bool
	deployment   model.Deployment
	resourceRefs map[string]string
	err          error
}

// workflowExecution separates application environment values from runtime network attachments.
type workflowExecution struct {
	bindings map[string]string
	networks []string
}
