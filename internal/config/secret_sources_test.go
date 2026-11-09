package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretSourceFile writes only test credentials into a private fixture directory.
func secretSourceFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".hostlier")
	// Preserve exact YAML bytes for strict parser assertions.
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestFlatSecretSources separates private bytes from public references and component identities.
func TestFlatSecretSources(t *testing.T) {
	path := secretSourceFile(t, "name: demo\nsecrets:\n  TOKEN: ' literal-private-value '\n  LOCAL: $LOCAL_TOKEN\n  STORED: secret://existing-token\n  DOLLAR: $$literal-dollar\nsidecars:\n  worker:\n    secrets:\n      TOKEN: sidecar-private-value\n")
	input, err := ReadProjectSources(path, "")
	// All flat source modes and nested member sources must parse together.
	if err != nil || len(input.Secrets) != 4 {
		t.Fatal("flat input rejected", err)
	}
	// Values retain whitespace and a literal escaped leading dollar.
	if *input.Secrets[0].Value != " literal-private-value " || input.Secrets[1].FromEnv != "LOCAL_TOKEN" || *input.Secrets[2].Value != "$literal-dollar" {
		t.Fatal("source bytes changed")
	}
	p := input.Project
	// Stored references are untouched, and primary and sidecar namespaces cannot collide.
	if p.Secrets["STORED"] != "existing-token" || p.Secrets["TOKEN"] == p.Sidecars["worker"].Secrets["TOKEN"] {
		t.Fatal("incorrect reference identity")
	}
	assertPrivateSourceSerialization(t, input)
	again, err := ReadProjectSources(path, "")
	// Re-reading unchanged desired state must use the same controller names.
	if err != nil || again.Project.Secrets["TOKEN"] != p.Secrets["TOKEN"] {
		t.Fatal("unstable generated name", err)
	}
}

// TestFlatSecretSourceErrorsArePrivate rejects ambiguity without including secret contents in errors.
func TestFlatSecretSourceErrorsArePrivate(t *testing.T) {
	cases := []string{
		"secrets: {TOKEN: {value: private-marker}}",
		"secrets: {TOKEN: ''}",
		"secrets: {TOKEN: '$private-marker; command'}",
		"secrets: {TOKEN: 'secret://private-marker/invalid'}",
		"secrets: {TOKEN: 123}",
		"secrets: {TOKEN: one, TOKEN: private-marker}",
		"unknown: private-marker",
		"secrets: {TOKEN: &value private-marker, OTHER: *value}",
		"secrets: {TOKEN: private-marker}\n---\nname: other",
	}
	// Each malformed source must preserve strict configuration parsing.
	for _, body := range cases {
		_, err := ReadProjectSources(secretSourceFile(t, "name: demo\n"+body), "")
		// Parser diagnostics must never echo rejected scalar contents.
		if err == nil || strings.Contains(err.Error(), "private-marker") {
			t.Fatal("unsafe configuration error", err)
		}
	}
}

// assertPrivateSourceSerialization checks the full wrapper for accidentally serialized inputs.
func assertPrivateSourceSerialization(t *testing.T, input ProjectInput) {
	t.Helper()
	data, _ := json.Marshal(input)
	// Neither literal values nor local environment lookups belong in plans or public output.
	for _, private := range []string{"literal-private-value", "sidecar-private-value", "LOCAL_TOKEN", "$literal-dollar"} {
		// Check the whole input wrapper, not just the normalized project.
		if strings.Contains(string(data), private) {
			t.Fatal("private source serialized")
		}
	}
}
