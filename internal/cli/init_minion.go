package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"hostlier/internal/model"
)

var minionName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// minionInitSetup retains authored metadata separately from the safe setup receipt.
type minionInitSetup struct {
	Original string
	Fields   map[string]json.RawMessage
	Changes  map[string]string
	Fused    *model.FusedConnection
	Secrets  map[string]string
}

// readMinionInitSetup preserves unknown config fields without including them in prompts or receipts.
func readMinionInitSetup(directory string) (*minionInitSetup, error) {
	data, err := readInitFile(filepath.Join(directory, "minion.json"))
	// Only a bounded regular config can participate in setup.
	if err != nil {
		return nil, err
	}
	setup := &minionInitSetup{Original: string(data), Changes: map[string]string{}}
	// Null, arrays and malformed documents are not editable configurations.
	if json.Unmarshal(data, &setup.Fields) != nil || setup.Fields == nil {
		return nil, fmt.Errorf("Invalid minion.json: expected a JSON object")
	}
	// Known setup fields must not silently replace authored values of another type.
	for _, key := range []string{"name", "entry", "outdir", "route", "architecture"} {
		var value string
		// Report only field names rather than their possibly sensitive contents.
		if raw, exists := setup.Fields[key]; exists && (string(raw) == "null" || json.Unmarshal(raw, &value) != nil) {
			return nil, fmt.Errorf("Minion %s must be a string", key)
		}
	}
	return setup, setup.readBindings()
}

// readBindings decodes reference types without exposing source contents in diagnostics.
func (setup *minionInitSetup) readBindings() error {
	// Fused discovery records only public identity; complete authored selections remain untouched.
	if raw, exists := setup.Fields["fused"]; exists && json.Unmarshal(raw, &setup.Fused) != nil {
		return fmt.Errorf("Invalid Minion fused settings")
	}
	// Absent secret bindings need no synthesized source-config field.
	if raw, exists := setup.Fields["secrets"]; exists {
		var values map[string]*string
		// Scalars, arrays and non-string references must not enter a deployable project.
		if json.Unmarshal(raw, &values) != nil {
			return fmt.Errorf("Minion secrets must map environment names to stored secret names")
		}
		setup.Secrets = map[string]string{}
		// A null member is not a usable stored secret name.
		for key, value := range values {
			// Reject null without including the rejected value or key in output.
			if value == nil {
				return fmt.Errorf("Minion secrets must map environment names to stored secret names")
			}
			setup.Secrets[key] = *value
		}
	}
	return nil
}

// value reads a previously type-checked public setup setting.
func (setup *minionInitSetup) value(key string) string {
	var value string
	_ = json.Unmarshal(setup.Fields[key], &value)
	return value
}

// set records explicit changes while retaining all unrelated authored JSON fields.
func (setup *minionInitSetup) set(key, value string) {
	// Existing matching settings need no rewrite or duplicate receipt entry.
	if setup.value(key) == value {
		return
	}
	raw, _ := json.Marshal(value)
	setup.Fields[key] = raw
	setup.Changes[key] = value
}

// configureInitMinions guides only apps selected for this deployment topology.
func (s *session) configureInitMinions(apps map[string]initApp, owners map[string]string) error {
	// Process only the deterministically selected lifecycle graph.
	for _, key := range sortedInitKeys(owners) {
		app := apps[key]
		// Other frameworks retain their existing discovery contracts.
		if app.Minion == nil {
			continue
		}
		// All answers are collected and validated before any filesystem mutation.
		if err := s.configureInitMinion(&app); err != nil {
			return err
		}
		apps[key] = app
	}
	return nil
}

// configureInitMinion fills build defaults and asks for the hostname that cannot be inferred safely.
func (s *session) configureInitMinion(app *initApp) error {
	setup := app.Minion
	// Validate the name even when pending Fused consent defers project validation.
	if !minionName.MatchString(app.Name) {
		return fmt.Errorf("Use an app name starting with a lowercase letter and containing only lowercase letters, numbers or hyphens")
	}
	setup.set("name", app.Name)
	setup.set("architecture", app.Project.Source.Container.Architecture)
	// Setup and artifact packaging must target the same server platform.
	if setup.value("architecture") != "amd64" && setup.value("architecture") != "arm64" {
		return fmt.Errorf("Minion architecture must be amd64 or arm64")
	}
	// An explicit CLI route has already been reviewed as the selected app override.
	if app.Project.Endpoint.Route != "" {
		setup.set("route", app.Project.Endpoint.Route)
	}
	if err := s.minionInitField(setup, "entry", "Server entry file", minionInitEntry(app.Directory)); err != nil {
		return err
	}
	if err := s.minionInitField(setup, "route", "Deployment hostname", ""); err != nil {
		return err
	}
	// Keep standalone builds usable for projects already pinned to a pre-protocol compiler.
	if script, err := findMinionCLI(app.Directory); err == nil {
		protocol, err := minionBuildProtocol(script)
		if err != nil {
			return err
		}
		if protocol == 0 {
			legacyMinionDefaults(setup)
		}
	}
	return finishMinionInit(app)
}

// minionInitEntry suggests conventional source files without running app code.
func minionInitEntry(directory string) string {
	// Conventional entry locations are suggestions, not directory traversal.
	for _, candidate := range []string{"app.ts", "src/app.ts", "app.tsx", "src/app.tsx"} {
		// Only an actual authored entry becomes the suggested default.
		if initFileExists(filepath.Join(directory, candidate)) {
			return candidate
		}
	}
	return ""
}

// minionInitField retries invalid terminal input while keeping unattended operation deterministic.
func (s *session) minionInitField(setup *minionInitSetup, key, label, fallback string) error {
	// Existing values never trigger redundant questions or automatic replacement.
	if value := setup.value(key); value != "" {
		return validateMinionInitField(key, value)
	}
	// Unattended callers receive deterministic defaults and actionable missing-input errors.
	if !s.interactive {
		// A hostname has no safe invented default; callers can provide the normal route flag.
		if fallback == "" {
			return fmt.Errorf("Minion setup needs %s; run hostlier init in a terminal or set it in minion.json (hostname: --route HOST)", key)
		}
		setup.set(key, fallback)
		return validateMinionInitField(key, fallback)
	}
	// Invalid answers can be corrected without restarting setup.
	for {
		answer, err := s.ask(label+minionInitDefaultLabel(fallback)+": ", false)
		// EOF and interruption discard the entire in-memory setup.
		if err != nil {
			return err
		}
		value := defaultValue(strings.TrimSpace(answer), fallback)
		// Invalid input remains within the wizard rather than requiring a full restart.
		if err = validateMinionInitField(key, value); err != nil {
			fmt.Fprintln(s.errOut, err)
			continue
		}
		setup.set(key, value)
		return nil
	}
}

// minionInitDefaultLabel describes Enter-to-accept only when a real default exists.
func minionInitDefaultLabel(value string) string {
	// A required hostname must not appear to have an empty accepted default.
	if value == "" {
		return ""
	}
	return " [" + value + "]"
}

// validateMinionInitField mirrors the compiler's public build constraints without printing rejected values.
func validateMinionInitField(key, value string) error {
	// Each public setting has its own build-time contract.
	switch key {
	case "entry": // Entry paths remain authored source references.
		// Paths cannot contain NUL or be omitted, but build remains responsible for loading code.
		if value == "" || strings.ContainsRune(value, 0) {
			return fmt.Errorf("Enter a server entry file")
		}
	case "route": // The compiler accepts one root HTTPS endpoint.
		return validateMinionInitRoute(value)
	}
	return nil
}

// validateMinionInitRoute accepts only the root HTTPS hostname supported by the Minion compiler.
func validateMinionInitRoute(value string) error {
	// Bare hostnames are the common interactive entry format.
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	// URL parsing failure must not expose the supplied value through the parser error.
	if err != nil {
		return fmt.Errorf("Enter a root HTTPS hostname without a port or path")
	}
	valid := minionInitRootURL(parsed)
	// Hostnames follow the same contract used by the TypeScript artifact generator.
	if !valid || !regexp.MustCompile(`(?i)^[a-z0-9.-]+\.[a-z]{2,}$`).MatchString(parsed.Hostname()) {
		return fmt.Errorf("Enter a root HTTPS hostname without a port or path")
	}
	return nil
}

// minionInitRootURL keeps URL structure checks separate from hostname validation.
func minionInitRootURL(parsed *url.URL) bool {
	return parsed.Scheme == "https" && parsed.Port() == "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Path == "" || parsed.Path == "/")
}

// finishMinionInit aligns the source deployment with the saved build settings and describes remaining work.
func finishMinionInit(app *initApp) error {
	setup := app.Minion
	route := setup.value("route")
	// An optional scheme is normalized for the generated browser-origin setting.
	if !strings.Contains(route, "://") {
		route = "https://" + route
	}
	parsed, _ := url.Parse(route)
	app.Project.Endpoint.Route = parsed.Hostname()
	app.Project.Env = map[string]string{"MINION_ORIGIN": "https://" + parsed.Hostname(), "PORT": fmt.Sprint(app.Project.Endpoint.Port)}
	app.Notes = append(app.Notes, "From "+app.Directory+", run hostlier deploy. It installs Minion if needed, builds the application, and requests Fused consent when required.")
	return nil
}

// minionInitPending detects an authored runtime binding awaiting its Engine-issued UUID.
func minionInitPending(p model.Project) bool { return p.Fused != nil && p.Fused.AppID == "" }

// minionInitUpdates merges selected config changes only after topology validation has succeeded.
func minionInitUpdates(apps map[string]initApp, owners map[string]string, files map[string]string) (map[string]string, map[string]map[string]string) {
	originals := map[string]string{}
	settings := map[string]map[string]string{}
	// Process only the deterministically selected lifecycle graph.
	for _, key := range sortedInitKeys(owners) {
		app := apps[key]
		// Unchanged and non-Minion configs remain byte-for-byte untouched.
		if app.Minion == nil || len(app.Minion.Changes) == 0 {
			continue
		}
		path := filepath.Join(app.Directory, "minion.json")
		data, _ := json.MarshalIndent(app.Minion.Fields, "", "  ")
		originals[path] = app.Minion.Original
		files[path] = string(data) + "\n"
		settings[path] = app.Minion.Changes
	}
	return originals, settings
}

// minionInitPreview excludes complete authored configs because arbitrary extension fields may hold private data.
func minionInitPreview(files, originals map[string]string) map[string]string {
	preview := map[string]string{}
	// Receipts contain new generated files only, never complete edited source configs.
	for path, value := range files {
		// Updated settings have a separate allowlisted receipt.
		if _, updated := originals[path]; !updated {
			preview[path] = value
		}
	}
	return preview
}

// minionInitCreated separates newly created deployment files from updated app metadata.
func minionInitCreated(files, originals map[string]string) []string {
	return sortedInitKeys(minionInitPreview(files, originals))
}
