package config

import (
	"hostlier/internal/model"
	"testing"
)

// TestAutoscalingRejectsUnsafeDelegation checks policy guards before runtime mutation.
func TestAutoscalingRejectsUnsafeDelegation(t *testing.T) {
	// Exercise each independent policy or recovery case with its own fixture state.
	for _, bad := range []string{"zero", "bounds", "request", "storage", "group", "cpu", "oversizedRequest"} {
		// Each subtest owns independent durable state and Kubernetes identities.
		t.Run(bad, func(t *testing.T) {
			p := model.Project{Version: 1, Name: "app", Image: "nginx", Port: 80, Route: "app.example.com", Requests: &model.Limits{CPU: "0.01", Memory: "32Mi"}, Resources: &model.Limits{CPU: "0.1", Memory: "64Mi"}, Autoscaling: &model.Autoscaling{MinReplicas: 1, MaxReplicas: 3, CPUTargetPercent: 65}}
			// Select the one policy or ownership fault exercised by this isolated scenario.
			switch bad {
			// Exercise explicit stopped capacity without permitting automatic wake.
			case "zero":
				p.Autoscaling.MinReplicas = 0
			// Reject a delegated replica interval that cannot constrain capacity safely.
			case "bounds":
				p.Autoscaling.MaxReplicas = 0
			// Reject delegation without its explicit CPU request denominator.
			case "request":
				p.Requests = nil
			// Reject automatic replica changes for a persistent single-writer application.
			case "storage":
				p.Volumes = map[string]model.Volume{"data": {Mount: "/data", Size: "1Gi"}}
			// Reject unsupported independent workload-group autoscaling.
			case "group":
				p.Version = 2
			// Reject a CPU target outside the reviewed utilization interval.
			case "cpu":
				p.Autoscaling.CPUTargetPercent = 101
			// Reject scheduling requests that exceed the approved resource limits.
			case "oversizedRequest":
				p.Requests.CPU = "1"
			}
			// Require the rejected or faulted operation to fail instead of silently granting authority.
			if _, err := ValidateProject(p); err == nil {
				t.Fatal("unsafe policy accepted", bad)
			}
		})
	}
}
