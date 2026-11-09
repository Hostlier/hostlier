package database_test

import (
	"encoding/json"
	"hostlier/packages/aiven"
	"hostlier/packages/database"
	"hostlier/packages/railway"
	"strings"
	"testing"
)

// base supplies valid identities with no credential values.
func base(engine, mode string) database.Config {
	c := database.Config{Version: 1, App: "analytics", Name: "primary", Engine: engine, Mode: mode, OutputSecret: "app-url"}
	// Named engines require an explicit database.
	if engine != "redis" && engine != "valkey" && engine != "opensearch" {
		c.Database = "analytics"
	}
	// MongoDB needs a concrete initial collection.
	if engine == "mongodb" {
		c.Collection = "events"
	}
	// Shared instances are addressed through protected administrative connections.
	if mode == database.NewDatabase {
		c.ConnectionSecret = "admin-url"
	}
	return c
}

// TestEngineModes verifies the whole advertised matrix against both provider validators.
func TestEngineModes(t *testing.T) {
	// Exercise every supported Aiven engine with both lifecycle modes.
	for _, capability := range aiven.Capabilities() {
		// New instances are valid for every advertised engine.
		for _, mode := range []string{database.NewInstance, database.NewDatabase} {
			c := aiven.Config{Config: base(capability.Engine, mode)}
			// Provider placement belongs only to instance provisioning.
			if mode == database.NewInstance {
				c.Instance = &aiven.Instance{TokenSecret: "token", Project: "project", Service: "service", Plan: "plan"}
			}
			checkMode(t, "aiven", capability, mode, c.Validate())
		}
	}
	// Railway's different engine matrix must obey the same lifecycle contract.
	for _, capability := range railway.Capabilities() {
		// Validate both service creation and shared-server database creation.
		for _, mode := range []string{database.NewInstance, database.NewDatabase} {
			c := railway.Config{Config: base(capability.Engine, mode)}
			// An external Hostlier server requires explicitly reviewed public connectivity.
			if mode == database.NewInstance {
				c.Instance = railwayInstance()
			}
			checkMode(t, "railway", capability, mode, c.Validate())
		}
	}
}

// railwayInstance constructs an explicit test provider target.
func railwayInstance() *railway.Instance {
	return &railway.Instance{TokenSecret: "token", TokenType: "project", Project: "project", Environment: "environment", Service: "service", Template: "template", PublicAccess: true}
}

// checkMode compares outcomes against a deliberately explicit engine capability.
func checkMode(t *testing.T, provider string, capability database.Capability, mode string, err error) {
	t.Helper()
	wantValid := mode == database.NewInstance || capability.NewDatabase
	// Rejected unsupported modes must not quietly fall back to provisioning instances.
	if (err == nil) != wantValid {
		t.Fatalf("%s %s %s: %v", provider, capability.Engine, mode, err)
	}
}

// TestInvalidConfigurations exercises ambiguous modes, misplaced fields and secret references.
func TestInvalidConfigurations(t *testing.T) {
	valid := base("postgres", database.NewDatabase)
	cases := []database.Config{valid, valid, valid, valid, valid, valid, valid, valid}
	cases[0].Mode = "existing"
	cases[1].ConnectionSecret = "postgres://admin:password@example.com/db"
	cases[2].OutputSecret = valid.ConnectionSecret
	cases[3].Database = "db; DROP DATABASE other"
	cases[4].Engine = "unsupported"
	cases[5].Collection = "ignored"
	cases[6].Version = 2
	cases[7].Mode = database.NewInstance
	// None of these configurations is allowed to produce an executable-looking preview.
	for i, c := range cases {
		_, err := (aiven.Config{Config: c}).Plan()
		// Each invalid configuration must fail before a plan is returned.
		if err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	_, err := (aiven.Config{Config: valid, Instance: &aiven.Instance{}}).Plan()
	// Existing connections must not hide unused instance creation settings.
	if err == nil {
		t.Fatal("mixed existing and new-instance settings accepted")
	}
}

// TestProviderRequirements verifies intentional account targeting and network exposure.
func TestProviderRequirements(t *testing.T) {
	c := railway.Config{Config: base("postgres", database.NewInstance), Instance: railwayInstance()}
	c.Instance.PublicAccess = false
	// Private Railway DNS cannot serve an ordinary external Hostlier installation.
	if c.Validate() == nil {
		t.Fatal("implicit public exposure accepted")
	}
	c.Instance.PublicAccess = true
	c.Instance.TokenType = "unknown"
	// Authentication mode must not choose an arbitrary token header.
	if c.Validate() == nil {
		t.Fatal("unknown token type accepted")
	}
	c.Instance.TokenType = "account"
	c.Instance.TokenSecret = c.OutputSecret
	// Publishing a connection must never overwrite provider credentials.
	if c.Validate() == nil {
		t.Fatal("credential collision accepted")
	}
	a := aiven.Config{Config: base("postgres", database.NewInstance), Instance: &aiven.Instance{TokenSecret: "token", Project: "project", Service: "service"}}
	// Plan selection is mandatory even when the provider has a default tier.
	if a.Validate() == nil {
		t.Fatal("missing paid plan accepted")
	}
}

// TestPreviewIdentity ensures redeploy identity is stable while configuration changes alter the fingerprint.
func TestPreviewIdentity(t *testing.T) {
	c := aiven.Config{Config: base("postgres", database.NewDatabase)}
	first, err := c.Plan()
	// The fixture must produce a valid preview.
	if err != nil {
		t.Fatal(err)
	}
	second, _ := c.Plan()
	// Identical input should be reviewable without unstable IDs.
	if first.Fingerprint != second.Fingerprint || first.Executable || !first.ApprovalRequired {
		t.Fatal("unstable or executable preview")
	}
	c.ConnectionSecret = "other-admin-url"
	changed, _ := c.Plan()
	// Connection changes require fresh review but cannot allocate another app dependency identity.
	if changed.Fingerprint == first.Fingerprint || changed.ResourceKey != first.ResourceKey {
		t.Fatal("incorrect identity or fingerprint")
	}
	encoded, _ := json.Marshal(first)
	// Previews must describe retention, shared ownership checks and protected publication.
	for _, required := range []string{"retain-on-app-delete", "check-ownership", "publish-secret", "before-app-deploy"} {
		// Guard workflow invariants as well as schema validation.
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("missing %s", required)
		}
	}
}

// TestStrictDecode rejects malformed documents without reproducing input secrets in diagnostics.
func TestStrictDecode(t *testing.T) {
	inputs := []string{
		"password: do-not-echo-this-secret", "version: 1\nversion: 2", "[]", "null",
		"version: 1\n---\nversion: 1", "version: &v 1\nname: *v", "version: [do-not-echo-this-secret",
		strings.Repeat("a", 65537),
	}
	// Invalid YAML cannot bypass strict schema validation through aliases or extra documents.
	for _, input := range inputs {
		var c aiven.Config
		err := database.Decode(strings.NewReader(input), &c)
		// Diagnostics should be useful without echoing sensitive source values.
		if err == nil || strings.Contains(err.Error(), "do-not-echo-this-secret") {
			t.Fatalf("unsafe decode result: %v", err)
		}
	}
}
