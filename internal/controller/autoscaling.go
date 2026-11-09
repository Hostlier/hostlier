package controller

import (
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// validatePlanProject revokes retained automatic authority before validating manual scale intent.
func validatePlanProject(b deploymentPlanRequest) (model.Project, error) {
	// Manual scale is an explicit override and cannot renew an old HPA policy.
	if b.Action == "scale" {
		b.Spec.SleepAfter = ""
		b.Spec.Autoscaling = nil
	}
	return config.ValidateProject(b.Spec)
}
