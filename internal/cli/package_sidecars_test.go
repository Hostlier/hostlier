package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/pkg/packagekit"
)

// sidecarContributionFixture models an authenticated service whose token is shared only with its parent.
func sidecarContributionFixture() packagekit.ProjectContribution {
	return packagekit.ProjectContribution{Version: 1,
		Sidecars: map[string]model.Project{"browser": {
			Source:   &model.Source{Image: "example/browser:1"},
			Endpoint: &model.Endpoint{Exposure: "loopback", Port: 9222},
			Health:   &model.Health{Exec: []string{"node", "/health.mjs"}},
			Secrets:  map[string]string{"SANDBOX_TOKEN": "browser-key"},
		}},
		Bindings: map[string]string{"BROWSER_URL": "sidecars.browser.url"},
		Secrets:  map[string]string{"BROWSER_TOKEN": "browser-key"},
	}
}

// TestContributedSidecarPlan exercises actual executable discovery, decoding, composition and validation.
func TestContributedSidecarPlan(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	// Contributor-only projects must work without a primary package builder.
	if err := os.Mkdir(filepath.Join(state, "packages"), 0700); err != nil {
		t.Fatal(err)
	}
	c := sidecarContributionFixture()
	data, _ := json.Marshal(c)
	script := "#!/bin/sh\ncase \"$1\" in\n describe) echo '{\"version\":1,\"name\":\"sandbox-browser\",\"configSchema\":{},\"outputSchema\":{},\"hooks\":[\"project-contribute\"]}' ;;\n project-contribute) cat <<'CONTRIBUTION'\n" + string(data) + "\nCONTRIBUTION\n;;\n *) exit 1 ;;\nesac\n"
	writeContractFile(t, filepath.Join(state, "packages", "sandbox-browser"), script)
	writeContractFile(t, filepath.Join(root, ".hostlier"), "version: 2\nname: agent\nsource: {image: example/agent:1}\nendpoint: {exposure: public, port: 8080, route: agent.example.com}\npackages:\n  sandbox-browser: {}\n")
	out, err := invoke([]string{"plan", "--state-dir", state, "--json"}, "", Options{CWD: root})
	// Both the endpoint binding and stored token reference must survive the local hook protocol.
	if err != nil || !strings.Contains(out, "sidecars.browser.url") || !strings.Contains(out, "BROWSER_TOKEN") {
		t.Fatal(out, err)
	}
}

// TestSidecarContributionConflicts prevents contributors from replacing authored or sibling workload authority.
func TestSidecarContributionConflicts(t *testing.T) {
	c := sidecarContributionFixture()
	p := model.Project{Sidecars: map[string]model.Project{"browser": {Source: &model.Source{Image: "other/image:1"}}}}
	// A conflict must identify ownership without printing the conflicting values.
	if err := mergeContributions(&p, map[string]packagekit.ProjectContribution{"sandbox-browser": c}); err == nil || !strings.Contains(err.Error(), "primary project and sandbox-browser") {
		t.Fatal(err)
	}
	p = model.Project{Secrets: map[string]string{"BROWSER_TOKEN": "other-secret"}}
	// A shared parent credential may not be silently rebound by an auxiliary package.
	if err := mergeContributions(&p, map[string]packagekit.ProjectContribution{"sandbox-browser": c}); err == nil || strings.Contains(err.Error(), "other-secret") {
		t.Fatal(err)
	}
	p = model.Project{}
	// Equal declarations coalesce and remain idempotent.
	if err := mergeContributions(&p, map[string]packagekit.ProjectContribution{"one": c, "two": c}); err != nil || len(p.Sidecars) != 1 {
		t.Fatal(err, p)
	}
}

// TestSidecarConnectionNamesReachBuilder exposes public destination names without credential values.
func TestSidecarConnectionNamesReachBuilder(t *testing.T) {
	g := enrolledPackages{contributions: map[string]packagekit.ProjectContribution{"sandbox-browser": sidecarContributionFixture()}}
	got := strings.Join(g.provided(), ",")
	// Both required parent inputs must be visible to a Minion or other primary builder.
	if got != "BROWSER_TOKEN,BROWSER_URL" {
		t.Fatal(got)
	}
}

// TestContributedSidecarValidation applies all existing workload boundaries after package composition.
func TestContributedSidecarValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*model.Project)
	}{
		// A package cannot publish a member independently of the parent.
		{"public sidecar", func(p *model.Project) { p.Endpoint.Exposure = "public"; p.Endpoint.Route = "browser.example.com" }},
		// Every member reserves its listener in the shared network namespace.
		{"port collision", func(p *model.Project) { p.Endpoint.Port = 8080 }},
		// Sidecars cannot own another deployment hierarchy.
		{"nested sidecar", func(p *model.Project) { p.Sidecars = map[string]model.Project{"nested": {}} }},
	}
	// Contribution fields must never bypass normal project validation.
	for _, tc := range cases {
		// Start each case with an independent valid contribution.
		t.Run(tc.name, func(t *testing.T) {
			c := sidecarContributionFixture()
			member := c.Sidecars["browser"]
			tc.edit(&member)
			c.Sidecars["browser"] = member
			p := hookService(model.Source{Image: "example/agent:1"})
			p.Workflow = nil
			// Composition alone is additive; validation must reject the completed invalid workload.
			if err := mergeContributions(&p, map[string]packagekit.ProjectContribution{"sandbox-browser": c}); err != nil {
				t.Fatal(err)
			}
			// Images remain public selectors here; artifact preparation happens during deploy.
			if err := config.ValidateInput(p); err == nil {
				t.Fatal("accepted invalid sidecar")
			}
		})
	}
}
