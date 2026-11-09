package config

import (
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
	"reflect"
	"testing"
)

// fusedProject describes a container with one independently scoped Engine identity.
func fusedProject() model.Project {
	return model.Project{Version: 2, Name: "mini", Source: &model.Source{Image: "example/mini:1.0.0"}, Endpoint: &model.Endpoint{Exposure: "public", Port: 8080, Route: "mini.example.com"}, Fused: &model.FusedConnection{GRPCURL: "https://engine.example:443", AppID: "11111111-2222-3333-4444-555555555555", TokenSecret: "mini-execution"}}
}

// TestFusedConnectionNormalization verifies rollback and review retain exact connection bindings.
func TestFusedConnectionNormalization(t *testing.T) {
	original := fusedProject()
	p, err := ValidateProject(original)
	// Verify the declared connection boundary without executing a deployment.
	if err != nil {
		t.Fatal(err)
	}
	// Verify the declared connection boundary without executing a deployment.
	if p.Env["FUSED_APP_ID"] != original.Fused.AppID || p.Env["FUSED_ENGINE_GRPC_URL"] != original.Fused.GRPCURL || p.Secrets["FUSED_APP_TOKEN"] != "mini-execution" {
		t.Fatal("missing scoped connection")
	}
	// Verify the declared connection boundary without executing a deployment.
	if original.Env != nil || original.Secrets != nil {
		t.Fatal("validation changed source maps")
	}
	second, err := ValidateProject(p)
	// Verify the declared connection boundary without executing a deployment.
	if err != nil || !reflect.DeepEqual(p, second) {
		t.Fatal("normalization is not stable", err)
	}

}

// TestFusedConnectionConflicts rejects ambiguous credentials before deployment.
func TestFusedConnectionConflicts(t *testing.T) {
	cases := map[string]func(*model.Project){
		// Mutate one producer to verify conflicting authority is rejected.
		"plaintext": func(p *model.Project) { p.Fused.GRPCURL = "http://engine.example:50051" },
		// Mutate one producer to verify conflicting authority is rejected.
		"userinfo": func(p *model.Project) { p.Fused.GRPCURL = "https://secret@engine.example" },
		// Mutate one producer to verify conflicting authority is rejected.
		"path": func(p *model.Project) { p.Fused.GRPCURL = "https://engine.example/api" },
		// Mutate one producer to verify conflicting authority is rejected.
		"query": func(p *model.Project) { p.Fused.GRPCURL = "https://engine.example?" },
		// Mutate one producer to verify conflicting authority is rejected.
		"invalid app": func(p *model.Project) { p.Fused.AppID = "latest" },
		// Mutate one producer to verify conflicting authority is rejected.
		"literal token": func(p *model.Project) { p.Env = map[string]string{"FUSED_APP_TOKEN": "token-value"} },
		// Mutate one producer to verify conflicting authority is rejected.
		"different engine": func(p *model.Project) {
			p.Env = map[string]string{"FUSED_ENGINE_GRPC_URL": "https://elsewhere.example"}
		},
		// Mutate one producer to verify conflicting authority is rejected.
		"different secret": func(p *model.Project) { p.Secrets = map[string]string{"FUSED_APP_TOKEN": "another-secret"} },
		// Mutate one producer to verify conflicting authority is rejected.
		"secret alias": func(p *model.Project) { p.Fused.TokenSecret = "../secret" },
		// Mutate one producer to verify conflicting authority is rejected.
		"workflow conflict": func(p *model.Project) {
			p.Workflow = &workflow.Spec{Steps: []workflow.Step{{ID: "connection", Package: "example", Config: map[string]any{}}}, Bindings: map[string]string{"FUSED_APP_TOKEN": "connection.token"}}
		},
	}
	// Verify the declared connection boundary without executing a deployment.
	for name, mutate := range cases {
		// Isolate each invalid declaration to preserve precise failure evidence.
		t.Run(name, func(t *testing.T) {
			p := fusedProject()
			mutate(&p)
			// Verify the declared connection boundary without executing a deployment.
			if _, err := ValidateProject(p); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}

// TestFusedSidecarIdentity ensures independent workload identities never inherit parent tokens.
func TestFusedSidecarIdentity(t *testing.T) {
	p := fusedProject()
	member := fusedProject()
	member.Name = ""
	member.Endpoint = &model.Endpoint{Exposure: "loopback", Port: 8081}
	member.Health = &model.Health{Exec: []string{"probe"}}
	member.Fused.TokenSecret = "sidecar-token"
	p.Sidecars = map[string]model.Project{"worker": member}
	actual, err := ValidateProject(p)
	// Verify the declared connection boundary without executing a deployment.
	if err != nil {
		t.Fatal(err)
	}
	// Verify the declared connection boundary without executing a deployment.
	if actual.Secrets["FUSED_APP_TOKEN"] != "mini-execution" || actual.Sidecars["worker"].Secrets["FUSED_APP_TOKEN"] != "sidecar-token" {
		t.Fatal("credential crossed workload boundary")
	}
}

// TestFusedIdentityReview binds review fingerprints to the immutable Engine version.
func TestFusedIdentityReview(t *testing.T) {
	p, err := ValidateProject(fusedProject())
	// The valid fixture must normalize before comparison.
	if err != nil {
		t.Fatal(err)
	}
	changed := fusedProject()
	changed.Fused.AppID = "22222222-2222-3333-4444-555555555555"
	next, err := ValidateProject(changed)
	// Verify the declared connection boundary without executing a deployment.
	if err != nil || Fingerprint(p) == Fingerprint(next) {
		t.Fatal("identity absent from review")
	}
}
