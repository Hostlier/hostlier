package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// minionWizardOptions supplies a deterministic terminal without contacting a controller.
func minionWizardOptions(t *testing.T, root string, answers []string) (Options, *[]string) {
	t.Helper()
	options := initOptions(t, root)
	questions := []string{}
	// Answers model one interactive session and record every displayed question.
	options.Prompt = func(question string, hidden bool) (string, error) {
		questions = append(questions, question)
		// Exhausted input represents cancellation rather than an accepted default.
		if len(answers) == 0 {
			return "", errors.New("cancelled setup")
		}
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	return options, &questions
}

// readWizardConfig reads the saved full config separately from the redacted command receipt.
func readWizardConfig(t *testing.T, root string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "minion.json"))
	// Setup must leave a readable JSON config for the Minion compiler.
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	// Invalid serialized settings would prevent the next build step.
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// TestMinionInitWizardDefaultsAndRetry demonstrates setup from a dev-only config without an output-directory question.
func TestMinionInitWizardDefaultsAndRetry(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": `{"name":"wizard","entry":"app.ts","extension":{"marker":"private-test-sentinel"}}`, "app.ts": "export default {};"})
	options, questions := minionWizardOptions(t, root, []string{"http://bad.example.com/path", "wizard.example.com"})
	output, err := invoke([]string{"init", "--detect"}, "", options)
	// Invalid hostname input stays inside the wizard and a corrected answer completes setup.
	if err != nil {
		t.Fatal(err)
	}
	assertWizardDefaults(t, root)
	project := initReadProject(t, filepath.Join(root, ".hostlier"))
	// The default build location matches Minion while the origin follows the explicit answer.
	if project.Source.Container.Context != "build/artifact" || project.Env["MINION_ORIGIN"] != "https://wizard.example.com" {
		t.Fatal(project)
	}
	// Receipts expose only newly selected public settings, never arbitrary config fields.
	if strings.Contains(output, "private-test-sentinel") || strings.Contains(strings.Join(*questions, " "), "outdir") || len(*questions) != 2 {
		t.Fatal(output, *questions)
	}
}

// assertWizardDefaults checks safe defaults separately from prompt and deployment behavior.
func assertWizardDefaults(t *testing.T, root string) {
	t.Helper()
	cfg := readWizardConfig(t, root)
	// Defaults remain optional and preserve every authored extension field.
	if cfg["image"] != nil || cfg["baseImage"] != nil || cfg["outdir"] != nil || cfg["extension"].(map[string]any)["marker"] != "private-test-sentinel" {
		t.Fatal(cfg)
	}
}

// TestMinionInitWizardCancelAndDryRun verifies all answers remain in memory until a successful real setup.
func TestMinionInitWizardCancelAndDryRun(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": `{"name":"wizard","entry":"app.ts"}`})
	before := initTree(t, root)
	options, _ := minionWizardOptions(t, root, []string{""})
	_, err := invoke([]string{"init", "--detect"}, "", options)
	// Interrupted prompts must not write half a config.
	if err == nil || !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("cancelled setup mutated files", err)
	}
	options, _ = minionWizardOptions(t, root, []string{"wizard.example.com"})
	output, err := invoke([]string{"init", "--detect", "--dry-run", "--json"}, "", options)
	// Preview includes proposed defaults but preserves exact authored bytes.
	if err != nil || !reflect.DeepEqual(before, initTree(t, root)) || !strings.Contains(output, "wizard.example.com") {
		t.Fatal(err, output)
	}
}

// TestMinionInitPendingConsentGuidesWithoutDroppingFused keeps the complete integration selection for the real build/provision flow.
func TestMinionInitPendingConsentGuidesWithoutDroppingFused(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": `{"name":"wizard","entry":"app.ts","fused":{"engineUrl":"http://127.0.0.1:28081","grpcUrl":"http://127.0.0.1:15051","tokenSecret":"test-token","services":{"probe":{"service":"readiness","version":"1.0.0","operations":["ReadStatus"]}}}}`})
	original := readWizardConfig(t, root)["fused"]
	output, err := invoke([]string{"init", "--route", "wizard.example.com"}, "", initOptions(t, root))
	// Pending identity is a successful setup state with a concrete next step, not a discarded integration.
	if err != nil || !strings.Contains(output, "hostlier deploy") {
		t.Fatal(err, output)
	}
	cfg := readWizardConfig(t, root)
	// No fake app ID or automatic credential authorization can enter the source configuration.
	if !reflect.DeepEqual(original, cfg["fused"]) {
		t.Fatal("changed Fused selection")
	}
	// The saved topology still needs a real controller-issued identity to pass validation.
	if _, err = invoke([]string{"validate"}, "", initOptions(t, root)); err == nil {
		t.Fatal("unbound project validated")
	}
	// The topology is available for review before consent.
	if _, err = os.Stat(filepath.Join(root, ".hostlier")); err != nil {
		t.Fatal(err)
	}
}

// TestMinionInitAuthoredSettingsAndConflict preserves existing choices and aborts before updating a config beside an existing deployment.
func TestMinionInitAuthoredSettingsAndConflict(t *testing.T) {
	raw := `{"name":"wizard","entry":"app.ts","image":"custom/wizard:2.0.0","baseImage":"node:24.21.0-alpine","route":"kept.example.com","outdir":"release","architecture":"arm64"}`
	root := initFixture(t, map[string]string{"minion.json": raw})
	_, err := invoke([]string{"init", "--detect"}, "", initOptions(t, root))
	// Fully configured apps do not need a terminal or a config rewrite.
	if err != nil || initTree(t, root)[filepath.Join(root, "minion.json")] != raw {
		t.Fatal(err)
	}
	project := initReadProject(t, filepath.Join(root, ".hostlier"))
	// Authored routes and output locations survive discovery.
	if project.Endpoint.Route != "kept.example.com" || project.Source.Container.Context != "release/artifact" || project.Source.Container.Architecture != "arm64" {
		t.Fatal(project)
	}
	before := initTree(t, root)
	_, err = invoke([]string{"init", "--route", "changed.example.com"}, "", initOptions(t, root))
	// A conflicting output prevents partial mutation of the existing build config.
	if err == nil || !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("conflict changed authored state", err)
	}
}

// TestMinionInitPreservesSecretReferences keeps runtime authentication bindings in generated deployments.
func TestMinionInitPreservesSecretReferences(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": `{"name":"wizard","entry":"app.ts","image":"wizard:0.1.0","baseImage":"node:24.21.0-alpine","route":"wizard.example.com","secrets":{"APP_TOKEN":"wizard-session-token"}}`})
	_, err := invoke([]string{"init", "--detect"}, "", initOptions(t, root))
	// A secret binding is a stored reference, not a reason to omit deployment setup.
	if err != nil {
		t.Fatal(err)
	}
	project := initReadProject(t, filepath.Join(root, ".hostlier"))
	// Existing references must survive the root deployment-file serialization.
	if project.Secrets["APP_TOKEN"] != "wizard-session-token" {
		t.Fatal("lost stored secret reference")
	}
}

// TestMinionInitRejectsMalformedSecrets fails without exposing authored values or updating source files.
func TestMinionInitRejectsMalformedSecrets(t *testing.T) {
	// Non-reference values must not pass simply because Fused consent is pending.
	for _, value := range []string{`["private-sentinel"]`, `{"TOKEN":123}`, `{"TOKEN":null}`} {
		root := initFixture(t, map[string]string{"minion.json": `{"name":"wizard","secrets":` + value + `}`})
		before := initTree(t, root)
		output, err := invoke([]string{"init", "--detect"}, "", initOptions(t, root))
		// Type failures preserve config bytes and name only the invalid public setting.
		if err == nil || !strings.Contains(err.Error(), "Minion secrets") || strings.Contains(output+err.Error(), "private-sentinel") || !reflect.DeepEqual(before, initTree(t, root)) {
			t.Fatal("malformed secret settings were not rejected privately", err)
		}
	}
}
