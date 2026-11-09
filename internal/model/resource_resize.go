package model

import "context"

// ResourceResizeManager exposes explicit allocation maintenance without changing storage or identity.
type ResourceResizeManager interface {
	// PlanResize validates an existing resource and returns its exact allocation preview.
	PlanResize(context.Context, ResourceSpec) (ResourcePlan, error)
	// Resize applies the reviewed allocation while retaining engine data and credentials.
	Resize(context.Context, ResourceSpec, ResourceContext) (Resource, error)
}
