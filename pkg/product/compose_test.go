package product

import (
	"hostlier/pkg/workflow"
	"testing"
)

// fixture defines a local package composition without provider services or executable source.
func fixture() Definition {
	return Definition{Version: 1, Name: "private-db", Revision: "1.0.0", Inputs: map[string]Input{"storage": {Type: "string", Required: true, Enum: []any{"1Gi", "2Gi"}}}, Secrets: []string{"admin"}, Steps: []Step{{Step: workflow.Step{ID: "db", Package: "postgres", Config: map[string]any{"mode": "new-instance"}}, ConfigInputs: map[string]string{"storage": "storage"}, SecretInputs: map[string]string{"connection": "admin"}}}, Bindings: map[string]string{"DATABASE_URL": "db.url"}}
}

// TestCompose verifies typed substitution, stored reference resolution and reusable definition isolation.
func TestCompose(t *testing.T) {
	definition := fixture()
	invocation := Invocation{Inputs: map[string]any{"storage": "2Gi"}, SecretRefs: map[string]string{"admin": "db-admin"}}
	result, err := Compose(definition, invocation)
	// A valid declaration becomes the ordinary bounded workflow schema.
	if err != nil || result.Steps[0].Config["storage"] != "2Gi" || result.Steps[0].Secrets["connection"] != "db-admin" {
		t.Fatal(result, err)
	}
	result.Steps[0].Config["mode"] = "changed"
	// Repeated composition may not retain mutable values from a prior invocation.
	if definition.Steps[0].Config["mode"] != "new-instance" {
		t.Fatal("definition mutated")
	}
}

// TestComposeRejectsUnknownAndInvalidInputs verifies failures happen before a workflow can be reviewed.
func TestComposeRejectsUnknownAndInvalidInputs(t *testing.T) {
	// Public values never coerce types, accept arbitrary settings or supply secret plaintext.
	for _, input := range []map[string]any{{}, {"storage": false}, {"storage": "100Gi"}, {"storage": "1Gi", "arbitrary": "script"}} {
		_, err := Compose(fixture(), Invocation{Inputs: input, SecretRefs: map[string]string{"admin": "db-admin"}})
		// Every invalid invocation must fail deterministically.
		if err == nil {
			t.Fatal("invalid invocation accepted", input)
		}
	}
}

// TestComposeRejectsExecutablePaths preserves the installed-package trust boundary.
func TestComposeRejectsExecutablePaths(t *testing.T) {
	definition := fixture()
	definition.Steps[0].Package = "/tmp/script.sh"
	_, err := Compose(definition, Invocation{Inputs: map[string]any{"storage": "1Gi"}, SecretRefs: map[string]string{"admin": "db-admin"}})
	// Local authors cannot add executable source by composing a product.
	if err == nil {
		t.Fatal("executable package path accepted")
	}
}
