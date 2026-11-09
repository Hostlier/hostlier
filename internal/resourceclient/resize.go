package resourceclient

import (
	"context"
	"hostlier/internal/model"
	"hostlier/pkg/resourceprotocol"
)

// PlanResize pins the package executable for an allocation maintenance plan.
func (m *Manager) PlanResize(ctx context.Context, s model.ResourceSpec) (model.ResourcePlan, error) {
	s.PackageDigest = ""
	v, digest, err := m.call(ctx, s.Type, "", resourceprotocol.Request{Operation: "plan-resize", Name: s.Name, Spec: s})
	v.Plan.Spec.PackageDigest = digest
	return v.Plan, err
}

// Resize requires the executable fingerprint approved in the allocation plan.
func (m *Manager) Resize(ctx context.Context, s model.ResourceSpec, c model.ResourceContext) (model.Resource, error) {
	// An allocation mutation requires a freshly reviewed executable.
	if s.PackageDigest == "" {
		return model.Resource{}, model.Err("RESOURCE_PACKAGE_PREFLIGHT", "Resource resize requires a fresh package plan")
	}
	v, _, err := m.call(ctx, s.Type, s.PackageDigest, request("resize", s.Name, s, c))
	emit(c, v.Events)
	return v.Resource, err
}
