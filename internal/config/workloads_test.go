package config

import (
	"hostlier/internal/model"

	"testing"
)

// workloadFixture declares an explicit public service and one private loopback dependency.
func workloadFixture() model.Project {
	return model.Project{Version: 2, Name: "web", Source: &model.Source{Image: "nginx:alpine"}, Endpoint: &model.Endpoint{Exposure: "public", Port: 8080, Route: "web.example.com"}, Sidecars: map[string]model.Project{"agent": {Source: &model.Source{Image: "agent:latest"}, Endpoint: &model.Endpoint{Exposure: "loopback", Port: 1907}, Health: &model.Health{Exec: []string{"probe"}}}}}
}

// TestGroupValidationRejectsAmbiguousTopology prevents publication, namespace, and writer mismatches.
func TestGroupValidationRejectsAmbiguousTopology(t *testing.T) {
	cases := map[string]func(*model.Project){
		"legacy":             func(p *model.Project) { p.Version = 1 },            // V1 cannot silently ignore a requested member.
		"conflicting source": func(p *model.Project) { p.Image = "other:latest" }, // Two sources cannot select different runnable content.
		"public member": func(p *model.Project) {
			m := p.Sidecars["agent"]
			m.Endpoint.Exposure = "public"
			p.Sidecars["agent"] = m
		}, // Members never claim their own ingress.
		"port collision":   func(p *model.Project) { m := p.Sidecars["agent"]; m.Endpoint.Port = 8080; p.Sidecars["agent"] = m }, // Loopback shares one port namespace.
		"missing probe":    func(p *model.Project) { m := p.Sidecars["agent"]; m.Health = nil; p.Sidecars["agent"] = m },         // Private readiness must be executable from inside the group.
		"empty executable": func(p *model.Project) { p.Command = []string{""} },                                                  // Invalid argv cannot reach a daemon.
		"sidecar multiwriter": func(p *model.Project) {
			n := 2
			p.Replicas = &n
			m := p.Sidecars["agent"]
			m.Volumes = map[string]model.Volume{"data": {Mount: "/data", Size: "1Gi"}}
			p.Sidecars["agent"] = m
		}, // A member volume also prevents unsafe replication.
	}
	// Each case starts from a fresh map so one mutation cannot influence another check.
	for name, change := range cases {
		t.Run(name, func(t *testing.T) { // Validate rejection at the public configuration boundary.
			p := workloadFixture()
			change(&p)
			// Invalid topology must fail before immutable image planning.
			if _, err := ValidateProject(p); err == nil {
				t.Fatal("invalid workload accepted")
			}
		})
	}
}

// TestNativeHarnestFieldsRemoved ensures the project reader rejects the retired built-in schema.
func TestNativeHarnestFieldsRemoved(t *testing.T) {
	for _, field := range []string{"source: {harnest: {artifact: agent}}", "profile: harnest", "sidecars: {agent: {source: {harnest: {artifact: agent}}}}"} {
		path := secretSourceFile(t, "name: demo\n"+field+"\n")
		// Installed packages use the packages section; old documents never select a built-in handler.
		if _, err := ReadProjectSources(path, ""); err == nil {
			t.Fatalf("accepted retired field %s", field)
		}
	}
}
