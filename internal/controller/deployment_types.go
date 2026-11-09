package controller

import (
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// Revision retains immutable rollback inputs from a completed deployment.
type Revision struct {
	ResourceRefs map[string]string    `json:"resourceRefs,omitempty"`
	ID           string               `json:"id"`
	Spec         model.Project        `json:"spec"`
	SecretRefs   map[string]SecretRef `json:"secretRefs"`
}

// Application combines active deployment state with retained rollback history.
type Application struct {
	ProductPolicy *workflow.ProductPolicy `json:"productPolicy,omitempty"`
	ResourceRefs  map[string]string       `json:"resourceRefs,omitempty"`
	model.Deployment
	Spec       model.Project        `json:"spec"`
	SecretRefs map[string]SecretRef `json:"secretRefs"`
	Revision   string               `json:"revision"`
	Revisions  []Revision           `json:"revisions"`
}

// Plan captures immutable actor-bound changes for explicit approval.
type Plan struct {
	StatefulSource  bool                      `json:"statefulSource,omitempty"`
	Workflow        []WorkflowPlan            `json:"workflow,omitempty"`
	RestoreBackupID string                    `json:"restoreBackupId,omitempty"`
	Kind            string                    `json:"kind,omitempty"`
	ResourceSpec    *model.ResourceSpec       `json:"resourceSpec,omitempty"`
	Dependencies    map[string]DependencyPlan `json:"dependencies,omitempty"`
	ID              string                    `json:"id"`
	Fingerprint     string                    `json:"fingerprint"`
	ExpiresAt       int64                     `json:"expiresAt"`
	Spec            model.Project             `json:"spec"`
	Changes         any                       `json:"changes"`
	Generation      int64                     `json:"generation"`
	SecretRefs      map[string]SecretRef      `json:"secretRefs"`
	Actor           string                    `json:"actor,omitempty"`
	OperationID     string                    `json:"operationId,omitempty"`
	RequestID       string                    `json:"requestId,omitempty"`
}

// Operation tracks durable accepted work and its terminal or recovery outcome.
type Operation struct {
	Workflow        []WorkflowPlan            `json:"workflow,omitempty"`
	SupersededBy    string                    `json:"supersededBy,omitempty"`
	RestoreBackupID string                    `json:"restoreBackupId,omitempty"`
	ResourceSpec    *model.ResourceSpec       `json:"resourceSpec,omitempty"`
	Dependencies    map[string]DependencyPlan `json:"dependencies,omitempty"`
	ID              string                    `json:"id"`
	Kind            string                    `json:"kind"`
	Name            string                    `json:"name"`
	Status          string                    `json:"status"`
	Spec            model.Project             `json:"spec,omitempty"`
	SecretRefs      map[string]SecretRef      `json:"secretRefs,omitempty"`
	Action          string                    `json:"action,omitempty"`
	Purge           bool                      `json:"purge,omitempty"`
	PlanID          string                    `json:"planId,omitempty"`
	Fingerprint     string                    `json:"fingerprint,omitempty"`
	Actor           string                    `json:"actor,omitempty"`
	Error           *model.Error              `json:"error,omitempty"`
}
