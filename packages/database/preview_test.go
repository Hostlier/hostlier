package database_test

import (
	"bytes"
	"testing"

	"hostlier/packages/aiven"
	"hostlier/packages/database"
	"hostlier/packages/railway"
)

// providerPreviewFixture supplies only the provider-specific capability list and valid instance placement.
type providerPreviewFixture struct {
	name         string
	capabilities []database.Capability
	plan         func(database.Config) (database.Plan, error)
}

// TestEveryEnginePreviewIsolatesApplicationCredentials prevents caches and search engines from publishing admin connections.
func TestEveryEnginePreviewIsolatesApplicationCredentials(t *testing.T) {
	// Both providers must enforce the same engine-independent publication boundary.
	for _, provider := range previewProviders() {
		// Exercise every advertised engine, including those without named databases.
		for _, capability := range provider.capabilities {
			plan, err := provider.plan(base(capability.Engine, database.NewInstance))
			// A supported instance configuration must produce a non-executable preview.
			if err != nil {
				t.Fatalf("%s/%s: %v", provider.name, capability.Engine, err)
			}
			checkCredentialWorkflow(t, plan, capability.NewDatabase)
		}
	}
}

// previewProviders keeps valid placement setup separate from the shared workflow assertions.
func previewProviders() []providerPreviewFixture {
	return []providerPreviewFixture{
		// Keep Aiven service placement explicit while varying engine-independent configuration.
		{name: "aiven", capabilities: aiven.Capabilities(), plan: func(c database.Config) (database.Plan, error) {
			return (aiven.Config{Config: c, Instance: &aiven.Instance{TokenSecret: "api-token", Project: "project", Service: "service", Plan: "plan"}}).Plan()
		}},
		// Supply Railway's reviewed external placement through the same preview-only contract.
		{name: "railway", capabilities: railway.Capabilities(), plan: func(c database.Config) (database.Plan, error) {
			return (railway.Config{Config: c, Instance: railwayInstance()}).Plan()
		}},
	}
}

// checkCredentialWorkflow checks ordering rather than mirroring individual generated detail strings.
func checkCredentialWorkflow(t *testing.T, plan database.Plan, named bool) {
	t.Helper()
	isolation, publish := stepIndex(plan.Steps, "ensure-application-credentials"), stepIndex(plan.Steps, "publish-secret")
	// Protect every engine's output publication behind an explicit credential-isolation step.
	if isolation < 0 || publish <= isolation || plan.Executable {
		t.Fatalf("unsafe credential workflow for %s", plan.Package)
	}
	// Existing named databases must prove ownership even when their parent instance was originally created by this app.
	if named {
		ownership, creation := stepIndex(plan.Steps, "check-ownership"), stepIndex(plan.Steps, "ensure-database")
		// Ownership precedes database reconciliation, which precedes application credential publication.
		if ownership < 0 || creation <= ownership || isolation <= creation {
			t.Fatalf("unsafe database ownership workflow for %s", plan.Package)
		}
	}
}

// stepIndex finds declarative step order without executing provider or database work.
func stepIndex(steps []database.Step, action string) int {
	// Return the first matching intent so duplicate publication cannot hide an earlier unsafe step.
	for index, step := range steps {
		// Only the requested semantic action establishes its ordering boundary.
		if step.Action == action {
			return index
		}
	}
	return -1
}

// TestPreviewSnapshotsMutableProviderSettings prevents edits to pointer-backed instance configuration from changing a reviewed preview.
func TestPreviewSnapshotsMutableProviderSettings(t *testing.T) {
	config := aiven.Config{Config: base("postgres", database.NewInstance), Instance: &aiven.Instance{TokenSecret: "token", Project: "project", Service: "service", Plan: "first"}}
	first, err := config.Plan()
	// Snapshot creation must succeed before testing pointer aliasing.
	if err != nil {
		t.Fatal(err)
	}
	snapshot := append([]byte(nil), first.Configuration...)
	config.Instance.Plan = "second"
	second, err := config.Plan()
	// An edited valid provider setting must remain reviewable as a new preview.
	if err != nil {
		t.Fatal(err)
	}
	// The first preview stays immutable while changed settings require review under the same durable identity.
	if !bytes.Equal(first.Configuration, snapshot) || first.Fingerprint == second.Fingerprint || first.ResourceKey != second.ResourceKey {
		t.Fatal("mutable provider settings invalidated preview snapshot or identity")
	}
}

// TestPreviewCopiesCallerSteps keeps later caller slice edits out of an already fingerprinted preview.
func TestPreviewCopiesCallerSteps(t *testing.T) {
	config := base("postgres", database.NewDatabase)
	steps := []database.Step{{Action: "publish-secret", Detail: "original"}}
	plan, err := database.Preview("aiven", config, config, steps)
	// A supported configuration representation must be snapshotted successfully.
	if err != nil {
		t.Fatal(err)
	}
	steps[0].Detail = "modified"
	// Workflow steps and configuration must both be owned by the returned preview.
	if plan.Steps[0].Detail != "original" {
		t.Fatal("preview retained caller-owned step storage")
	}
}
