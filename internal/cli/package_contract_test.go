package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// readContractFile keeps fixture failures separate from schema assertions.
func readContractFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	// The actual hook wire request or authored manifest must exist.
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// writeContractFile installs a fixture or changes an operator-supplied value.
func writeContractFile(t *testing.T, path, data string) {
	t.Helper()
	// Executable fixtures require owner execute permission.
	if err := os.WriteFile(path, []byte(data), 0700); err != nil {
		t.Fatal(err)
	}
}

// TestLocalContractInputs runs real hook processes, including packaging, with one captured config.
func TestLocalContractInputs(t *testing.T) {
	root, state := writeHookFixture(t, `["project-plan","project-package","project-contribute"]`)
	path := filepath.Join(state, "packages", "lantern")
	script := readContractFile(t, path)
	script = strings.Replace(script, `"configSchema":{}`, `"configSchema":{"type":"object","required":["region"],"additionalProperties":false,"properties":{"region":{"type":"string","enum":["west","east"]},"replicas":{"type":"integer","default":2},"label":{"type":"string","default":"001"}}}`, 1)
	script = strings.Replace(script, "project-plan)", `project-contribute) cat > contribute-input.json; echo '{"version":1}' ;;
project-plan)`, 1)
	writeContractFile(t, path, script)
	project := filepath.Join(root, ".hostlier")
	writeContractFile(t, project, "version: 2\npackages:\n  lantern: {region: west} # keep my settings\n")
	opts := Options{CWD: root}
	args := []string{"package", "add", "lantern", "--state-dir", state}
	// Repeat add must hydrate an existing namespace and then preserve exact bytes.
	if _, err := invoke(args, "", opts); err != nil {
		t.Fatal(err)
	}
	first := readContractFile(t, project)
	// A repeat must preserve the existing manifest byte for byte.
	if _, err := invoke(args, "", opts); err != nil || first != readContractFile(t, project) {
		t.Fatal("non-idempotent enrollment", err)
	}
	testProposal(t, root, hookService(model.Source{Container: &model.ContainerSource{Context: ".", Architecture: "amd64"}}))
	// The regular plan performs contribution and planning in separate processes.
	if _, err := invoke([]string{"plan", "--state-dir", state}, "", opts); err != nil {
		t.Fatal(err, first)
	}
	assertHookConfig(t, root, "contribute-input.json")
	assertHookConfig(t, root, "plan-input.json")
	checkPackageWireConfig(t, root, state)
}

// assertHookConfig checks that defaults keep their types on the JSON wire.
func assertHookConfig(t *testing.T, root, file string) {
	t.Helper()
	var req struct{ Config map[string]any }
	// Decode as the executable would observe it, independently of the SDK struct.
	if err := json.Unmarshal([]byte(readContractFile(t, filepath.Join(root, file))), &req); err != nil {
		t.Fatal(err)
	}
	// String-looking numbers must survive YAML generation as strings.
	if req.Config["region"] != "west" || req.Config["replicas"] != float64(2) || req.Config["label"] != "001" {
		t.Fatal(req)
	}
}

// checkPackageWireConfig uses the same enrollment path as deploy and captures the artifact hook input.
func checkPackageWireConfig(t *testing.T, root, state string) {
	t.Helper()
	s := session{cwd: root, ctx: t.Context(), flags: map[string]string{"state-dir": state}}
	_, authored, err := s.projectHookSelection()
	// Use the generated project as the deployment input.
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.loadEnrollment("", authored)
	// The package is installed and its declared config should already be validated.
	if err != nil {
		t.Fatal(err)
	}
	defer group.close()
	_, err = group.hooks["lantern"].Package(t.Context(), t.TempDir(), "amd64", json.RawMessage("{}"), nil)
	// The hook returns a real build context; no image build or upload is necessary.
	if err != nil {
		t.Fatal(err)
	}
	assertHookConfig(t, root, "package-input.json")
}

// checkNativeEnrollment exercises the real postgres binary's published contract.
func checkNativeEnrollment(t *testing.T, root, state string) {
	t.Helper()
	path := filepath.Join(root, ".hostlier")
	args := []string{"package", "add", "postgres", "--state-dir", state}
	opts := Options{CWD: root}
	// An unavailable package cannot create or modify the project.
	if _, err := invoke([]string{"package", "add", "postgres", "--state-dir", filepath.Join(root, "absent")}, "", opts); err == nil {
		t.Fatal("added an uninstalled package")
	}
	// Existing empty enrollments from older clients can still be populated.
	writeContractFile(t, path, "version: 2\npackages:\n  postgres: {}\n")
	// Re-adding the installed package must hydrate the empty entry.
	if _, err := invoke(args, "", opts); err != nil {
		t.Fatal(err)
	}
	raw := readContractFile(t, path)
	// The scaffold shows choices, references and output names without choosing any for the operator.
	for _, field := range []string{"mode: null", "new-instance", "connection: null", "bindings: {}", "url"} {
		// Every declared field is discoverable in the generated file.
		if !strings.Contains(raw, field) {
			t.Fatalf("missing %q in %s", field, raw)
		}
	}
	plan := []string{"plan", "--state-dir", state, "--json"}
	// Required placeholders are actionable even before the rest of the app is configured.
	if _, err := invoke(plan, "", opts); err == nil || !strings.Contains(err.Error(), "packages.postgres.config.mode is required") {
		t.Fatal(err)
	}
	expectInvalidMode(t, path, raw, plan, opts)
	edited := strings.Replace(raw, "mode: null", "mode: new-instance", 1)
	edited = strings.Replace(edited, "bindings: {}", "bindings: {DATABASE_URL: url}", 1)
	writeContractFile(t, path, edited+"name: demo\nsource: {image: 'example/demo:v1'}\nendpoint: {port: 8000, exposure: public, route: demo.example.com}\n")
	out, err := invoke(plan, "", opts)
	// Local planning must produce exactly the selected reference and approved configuration.
	if err != nil || !strings.Contains(out, `"DATABASE_URL":"postgres.url"`) || !strings.Contains(out, `"config":{"mode":"new-instance"}`) {
		t.Fatal(out, err)
	}
}

// expectInvalidMode verifies that enum failures identify the input but omit its value.
func expectInvalidMode(t *testing.T, path, raw string, plan []string, opts Options) {
	t.Helper()
	writeContractFile(t, path, strings.Replace(raw, "mode: null", "mode: PRIVATE_REJECTED_TEXT", 1))
	_, err := invoke(plan, "", opts)
	// Reject invalid settings before they reach any controller or appear in a diagnostic.
	if err == nil || !strings.Contains(err.Error(), "/mode") || strings.Contains(err.Error(), "PRIVATE_REJECTED_TEXT") {
		t.Fatal(err)
	}
}
