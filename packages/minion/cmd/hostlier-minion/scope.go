package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"os/exec"

	"hostlier/packages/minion/src/selection"
)

// declarations ties Fused permissions to their intended Engine.
type declarations struct {
	EngineURL string          `json:"engineUrl"`
	Manifest  json.RawMessage `json:"manifest"`
}

//go:embed plan-scope.mjs
var scopeProgram string

// consentScope distinguishes new permissions from a reference to an existing app grant.
func consentScope(root string, app projectFile) (json.RawMessage, error) {
	// An app without Fused requests no grant.
	if app.Fused == nil {
		return nil, nil
	}
	var selectionConfig struct {
		Services  json.RawMessage `json:"services"`
		EngineURL string          `json:"engineUrl"`
	}
	// Inspect the raw selection because app identity alone does not describe requested access.
	if err := json.Unmarshal(app.raw["fused"], &selectionConfig); err != nil {
		return nil, errors.New("Invalid Fused declarations")
	}
	// Existing app bindings without a new selection refer to the existing grant.
	if len(selectionConfig.Services) == 0 && app.Fused.AppID != "" {
		return nil, nil
	}
	script := ""
	// Legacy apps declare their operations in source instead of minion.json.
	if len(selectionConfig.Services) == 0 {
		script = findCompiler(root)
		// Use the pinned compiler's static analyzer for that format.
		if script == "" {
			return nil, errors.New("Install the pinned Minion compiler with hostlier minion install to inspect code-defined Fused permissions")
		}
	}
	return runScopeCompiler(root, script, app)
}

// runScopeCompiler inspects the captured config offline; neither application modules nor a catalog endpoint are executed.
func runScopeCompiler(root, script string, app projectFile) (json.RawMessage, error) {
	cmd := exec.Command("node", "--input-type=module-typescript", "--eval", selection.Rules+"\n"+scopeProgram, root, script)
	cmd.Dir = root
	snapshot, err := json.Marshal(app.raw)
	// Run the analyzer on the captured config that produced the review.
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(snapshot)
	cmd.Env = withoutFusedCredentials()
	data, err := cmd.Output()
	// Return a public diagnostic without exposing paths or data from the analyzer.
	if err != nil {
		return nil, errors.New("Cannot plan Fused permissions; verify the pinned services, operations, bucket and Engine origin")
	}
	// Only bounded JSON declarations can be reviewed and compared later.
	if len(data) > 70<<10 || !json.Valid(data) {
		return nil, errors.New("Invalid Fused plan declarations")
	}
	return bytes.TrimSpace(data), nil
}
