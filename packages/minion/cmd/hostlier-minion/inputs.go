package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// buildInputSchema advertises public build choices. Omitted overrides inherit the app's manifest.
func buildInputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"name":      map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$", "description": "App name. Required unless minion.json already names the app."},
			"entry":     map[string]any{"type": "string", "minLength": 1, "description": "Project-relative server entry file. Omit to use minion.json or the compiler's app.ts / src/app.ts discovery."},
			"baseImage": map[string]any{"type": "string", "pattern": `^node:(24|2[5-9]|[3-9][0-9])\.[0-9]+\.[0-9]+(-[a-z0-9.-]+)?(@sha256:[a-f0-9]{64})?$`, "description": "Concrete Node 24+ image used in the Dockerfile. Omit to inherit the app or compiler default."},
		},
	}
}

// mergeBuildInputs overlays only declared package inputs onto the compiler's manifest snapshot.
func mergeBuildInputs(root string, app projectFile, inputs map[string]any) (projectFile, error) {
	// Preserve all manifest fields unless the package explicitly overrides them.
	for key, value := range inputs {
		text, ok := value.(string)
		// The CLI normally validates this contract before invoking the hook.
		if !ok || text == "" {
			return app, errors.New("Minion package inputs must be nonempty strings")
		}
		// Validate source ownership and identity before changing the compiler snapshot.
		if err := app.setBuildInput(root, key, text); err != nil {
			return app, err
		}
		app.raw[key], _ = json.Marshal(text)
	}
	// A package-only project still needs a stable app identity.
	if app.Name == "" {
		return app, errors.New("set packages.minion.name in .hostlier or name in minion.json")
	}
	return app, nil
}

// setBuildInput checks each override against the on-disk source before the plan is published.
func (app *projectFile) setBuildInput(root, key, text string) error {
	// Only these fields may override compiler settings in a project.
	switch key {
	// An installed app's existing identity must agree with the deployment.
	case "name":
		// App identity must remain stable across initialization, plan and build.
		if app.Name != "" && app.Name != text {
			return errors.New("packages.minion.name must match minion.json")
		}
		app.Name = text
	// Resolve project-relative source paths beside the original manifest.
	case "entry":
		// Absolute paths and parent traversal cannot identify this project’s entry.
		if !filepath.IsLocal(text) || filepath.Clean(text) == "." {
			return errors.New("packages.minion.entry must be a project-relative file")
		}
		info, err := os.Stat(filepath.Join(root, text))
		// Fail during planning when the selected entry cannot be bundled.
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("packages.minion.entry must identify an existing source file")
		}
		app.Entry = text
	// The compiler validates image references again before generating a Dockerfile.
	case "baseImage":
		return nil
		// Other keys are not part of the public contract.
	default:
		return errors.New("unknown Minion package input")
	}
	return nil
}

// standaloneApp resolves the same conventional entries as the Minion compiler for a manifest-free app.
func standaloneApp(root string, inputs map[string]any) (projectFile, error) {
	app, err := mergeBuildInputs(root, projectFile{raw: map[string]json.RawMessage{}}, inputs)
	// An explicit entry was already verified against the project directory.
	if err != nil || app.Entry != "" {
		return app, err
	}
	// Resolve the source during planning so a missing server file cannot pass review.
	for _, entry := range []string{"app.ts", "src/app.ts", "app.tsx", "src/app.tsx"} {
		info, err := os.Stat(filepath.Join(root, entry))
		// Freeze the first existing regular entry in the compiler snapshot.
		if err == nil && info.Mode().IsRegular() {
			app.Entry = entry
			app.raw["entry"], _ = json.Marshal(entry)
			return app, nil
		}
	}
	return app, errors.New("set packages.minion.entry to an existing server source file")
}
