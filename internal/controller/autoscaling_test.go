package controller

import (
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
	"testing"
)

// TestAutoscalingRetainedPolicyIntersection prevents delegated capacity escaping either approved policy.
func TestAutoscalingRetainedPolicyIntersection(t *testing.T) {
	spec := project()
	spec.Requests = &model.Limits{CPU: "0.01", Memory: "32Mi"}
	spec.Resources = &model.Limits{CPU: "0.1", Memory: "64Mi"}
	spec.Autoscaling = &model.Autoscaling{MinReplicas: 1, MaxReplicas: 3, CPUTargetPercent: 65}
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	c.state.Applications[spec.Name] = &Application{Spec: spec, ProductPolicy: &workflow.ProductPolicy{MaxReplicas: 2}}
	// The retained ceiling applies even if the proposed workflow advertises wider bounds.
	if err := c.validateProductPolicy(spec); err == nil {
		t.Fatal("delegation escaped retained maximum")
	}
	spec.Workflow = &workflow.Spec{ProductPolicy: &workflow.ProductPolicy{MaxReplicas: 4}}
	// Require the rejected or faulted operation to fail instead of silently granting authority.
	if err := c.validateProductPolicy(spec); err == nil {
		t.Fatal("replacement workflow broadened retained delegation")
	}
	spec.Autoscaling.MaxReplicas = 2
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err := c.validateProductPolicy(spec); err != nil {
		t.Fatal(err)
	}
	// A newly reviewed tighter policy also participates in the intersection.
	spec.Workflow.ProductPolicy.MaxReplicas = 1
	// Require the rejected or faulted operation to fail instead of silently granting authority.
	if err := c.validateProductPolicy(spec); err == nil {
		t.Fatal("new tighter maximum ignored")
	}
}

// TestManualScaleAndRollbackRevokeAutoscaling ensures ordinary review never implicitly resumes delegation.
func TestManualScaleAndRollbackRevokeAutoscaling(t *testing.T) {
	spec := project()
	spec.Requests = &model.Limits{CPU: "0.01", Memory: "32Mi"}
	spec.Resources = &model.Limits{CPU: "0.1", Memory: "64Mi"}
	spec.Autoscaling = &model.Autoscaling{MinReplicas: 1, MaxReplicas: 3, CPUTargetPercent: 65}
	zero := 0
	spec.Replicas = &zero
	manual, err := validatePlanProject(deploymentPlanRequest{Spec: spec, Action: "scale"})
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil || manual.Autoscaling != nil || model.DesiredReplicas(manual) != 0 {
		t.Fatal("manual zero renewed delegation", err)
	}
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	spec.Replicas = nil
	c.state.Applications[spec.Name] = &Application{Spec: spec, Revisions: []Revision{{ID: "old", Spec: spec}}}
	restored, _, err := c.rollbackSpecification(spec, "old")
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil || restored.Autoscaling != nil {
		t.Fatal("rollback renewed autonomous authority", err)
	}
}
