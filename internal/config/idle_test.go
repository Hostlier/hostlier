package config

import (
	"hostlier/internal/model"
	"testing"
)

// TestSleepAfterContract validates the normalized policy and its deployment boundaries.
func TestSleepAfterContract(t *testing.T) {
	p := model.Project{Version: 1, Name: "web", Image: "nginx", Port: 80, Route: "web.example.test", SleepAfter: "120s"}
	got, err := ValidateProject(p)
	// Canonical durations keep equivalent deployment fingerprints stable.
	if err != nil || got.SleepAfter != "2m0s" {
		t.Fatal(got.SleepAfter, err)
	}
	// Each mutation violates a different idle policy constraint.
	for _, change := range []func(*model.Project){
		// Require an explicit interval unit.
		func(p *model.Project) { p.SleepAfter = "soon" },
		// Reject a negative idle interval.
		func(p *model.Project) { p.SleepAfter = "-10m" },
		// One hostname currently identifies one wakeable service.
		func(p *model.Project) { p.Route += "/prefix" },
		// A manually approved zero must never wake on demand.
		func(p *model.Project) { zero := 0; p.Replicas = &zero },
		// Idle scaling cannot stop a persistent writer.
		func(p *model.Project) { p.Volumes = map[string]model.Volume{"disk": {Mount: "/data", Size: "1Gi"}} },
		// Only one policy can own replica count.
		func(p *model.Project) { p.Autoscaling = &model.Autoscaling{MinReplicas: 1, MaxReplicas: 2} },
	} {
		copy := p
		change(&copy)
		// None of these cases may grant automatic authority.
		if _, err := ValidateProject(copy); err == nil {
			t.Fatal("invalid idle policy accepted", copy)
		}
	}
}
