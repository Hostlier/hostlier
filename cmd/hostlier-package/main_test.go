package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/packages/database"
)

// TestExamplePlans exercises file loading, strict decoding, provider validation and JSON output together.
func TestExamplePlans(t *testing.T) {
	// Both independently packaged providers support both lifecycle modes.
	for _, provider := range []string{"aiven", "railway"} {
		// Repository examples are executable contract fixtures for the local command.
		for _, mode := range []string{"new-instance", "new-database"} {
			var output bytes.Buffer
			path := filepath.Join("..", "..", "packages", provider, "examples", mode+".yaml")
			// The command must accept every documented configuration example.
			if err := run([]string{"plan", provider, path}, &output); err != nil {
				t.Fatal(err)
			}
			var plan database.Plan
			// Output must be structured and explicitly incapable of live execution.
			if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
				t.Fatal(err)
			}
			// No provider operation may be implied by an offline configuration preview.
			if plan.Package != provider || plan.Executable || !plan.ApprovalRequired || len(plan.Steps) == 0 {
				t.Fatal("invalid plan contract")
			}
		}
	}
}

// TestCommandRejectsApply makes unsupported execution fail closed.
func TestCommandRejectsApply(t *testing.T) {
	// No invocation can accidentally create a cloud resource.
	for _, args := range [][]string{{}, {"apply", "aiven", "config.yaml"}, {"plan", "unknown", "config.yaml"}} {
		var output bytes.Buffer
		// Invalid commands must not emit partial plans.
		if run(args, &output) == nil || output.Len() != 0 {
			t.Fatal("invalid command accepted")
		}
	}
}

// TestCommandRejectsInvalidFiles checks the file-to-error boundary without leaking configuration contents.
func TestCommandRejectsInvalidFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	inputs := []string{"password: do-not-echo-this-secret", "version: [do-not-echo-this-secret", "version: 99"}
	// Missing files, invalid YAML, unknown fields and semantic errors must all produce no preview.
	assertRejectedFile(t, path)
	// Reuse the same protected path so each case exercises the complete command entry point.
	for _, input := range inputs {
		// Protect even deliberately invalid fixture contents while testing error redaction.
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		assertRejectedFile(t, path)
	}
}

// assertRejectedFile checks that rejected input cannot leak through either the plan or error channel.
func assertRejectedFile(t *testing.T, path string) {
	t.Helper()
	var output bytes.Buffer
	err := run([]string{"plan", "aiven", path}, &output)
	// A failure must remain an error and leave stdout empty for machine consumers.
	if err == nil || output.Len() != 0 {
		t.Fatalf("invalid file produced a preview: error=%v", err)
	}
	// Malformed secret-bearing input must not be copied into diagnostics.
	if strings.Contains(err.Error(), "do-not-echo-this-secret") {
		t.Fatal("configuration contents leaked through error")
	}
}

// rejectedOutput simulates a closed output pipe without depending on OS pipe timing.
type rejectedOutput struct{ err error }

// Write preserves the sentinel output error so the command cannot report false success.
func (w rejectedOutput) Write([]byte) (int, error) { return 0, w.err }

// TestCommandReportsOutputFailure ensures a valid plan is not reported successful when writing fails.
func TestCommandReportsOutputFailure(t *testing.T) {
	want := errors.New("output unavailable")
	path := filepath.Join("..", "..", "packages", "aiven", "examples", "new-database.yaml")
	err := run([]string{"plan", "aiven", path}, rejectedOutput{err: want})
	// Preserve the underlying writer failure for callers deciding whether output was delivered.
	if !errors.Is(err, want) {
		t.Fatalf("output failure lost: %v", err)
	}
}
