package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"hostlier/internal/model"
	"hostlier/pkg/packagekit"
)

// fixture creates a private project file in the test directory.
func fixture(t *testing.T, path, content string) {
	t.Helper()
	// Mirror nested project and compiler layouts in a fresh root.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	// Surface setup failures before running the hook assertions.
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestMinionHookPreservesReviewedHostlierTopology verifies author settings survive staging.
func TestMinionHookPreservesReviewedHostlierTopology(t *testing.T) {
	root := t.TempDir()
	fixture(t, filepath.Join(root, "minion.json"), `{"name":"read","outdir":"custom","extension":{"preserved":true}}`)
	fixture(t, filepath.Join(root, ".hostlier"), `version: 2
name: read
package: minion
source:
  container:
    context: custom/artifact
    architecture: amd64
endpoint:
  route: review.example.com
  port: 8081
  exposure: public
env:
  APP_MODE: stable
secrets:
  APP_SECRET: secret://reference
`)
	target := packagekit.ProjectPlanRequest{Version: 1, Architecture: "arm64", ProjectDir: root}
	res, err := plan(target)
	// The custom build context must still allow the requested architecture.
	if err != nil {
		t.Fatal(err)
	}
	var p model.Project
	// Check the project representation the generic CLI actually receives.
	if err := json.Unmarshal(res.Project, &p); err != nil {
		t.Fatal(err)
	}
	// Compare the derived container fields alongside the author's endpoint, env and secret.
	for field, pair := range map[string][2]string{
		"context":          {p.Source.Container.Context, "."},
		"architecture":     {p.Source.Container.Architecture, "arm64"},
		"port":             {strconv.Itoa(p.Endpoint.Port), "8081"},
		"secret reference": {p.Secrets["APP_SECRET"], "reference"},
		"mode":             {p.Env["APP_MODE"], "stable"},
		"origin":           {p.Env["MINION_ORIGIN"], "https://review.example.com"},
	} {
		// Identify the affected field if a topology setting was lost.
		if pair[0] != pair[1] {
			t.Errorf("%s: got %q; want %q", field, pair[0], pair[1])
		}
	}
	p.Secrets["EXTRA"] = "not-reviewed"
	tampered, _ := json.Marshal(p)
	_, err = build(packagekit.ProjectPackageRequest{Version: 1, ProjectDir: root, Architecture: "arm64", OutputDir: t.TempDir(), Project: tampered})
	// An extra secret reference invalidates the review before compiler installation.
	if err == nil || !strings.Contains(err.Error(), "declarations changed") {
		t.Fatal(err)
	}
}

// A compromised or stale compiler cannot widen Fused consent between plan and packaging.
func TestMinionHookChecksCompiledFusedManifest(t *testing.T) {
	requireNode24(t)
	root := t.TempDir()
	raw := `{"name":"read","route":"read.example.com","fused":{"engineUrl":"https://engine.example.com","grpcUrl":"https://engine.example.com","tokenSecret":"stored-token","version":"1.0.0","bucket":"chosen","services":{"api":{"service":"api","version":"v1","operations":["read"]}}}}`
	cfg := filepath.Join(root, "minion.json")
	fixture(t, cfg, raw)
	res, err := plan(packagekit.ProjectPlanRequest{Version: 1, ProjectDir: root, Architecture: "amd64"})
	// Declarative Fused planning works with no installed project dependencies.
	if err != nil {
		t.Fatal(err)
	}
	// The proposal is read without node_modules, catalog credentials, or app source evaluation.
	var planned declarations
	// Check that the reviewed bucket is associated with the expected Engine.
	if err := json.Unmarshal(res.Declarations, &planned); err != nil || planned.EngineURL != "https://engine.example.com" || !strings.Contains(string(planned.Manifest), `"bucket":"chosen"`) {
		t.Fatal(string(res.Declarations), err)
	}
	req := packagekit.ProjectPackageRequest{Version: 1, ProjectDir: root, Architecture: "amd64", OutputDir: t.TempDir(), Project: res.Project, Declarations: res.Declarations}
	fixture(t, cfg, strings.Replace(raw, `"chosen"`, `"changed"`, 1))
	// Reject a new bucket even when the project's other settings still match.
	if _, err := build(req); err == nil || !strings.Contains(err.Error(), "Fused declarations changed") {
		t.Fatal(err)
	}
	fixture(t, cfg, raw)
	installed := filepath.Join(root, "node_modules", "@fused", "minion")
	fixture(t, filepath.Join(installed, "package.json"), `{"hostlier":{"minionBuildProtocol":1}}`)
	fixture(t, filepath.Join(installed, "dist/src/cli.js"), `const fs=require('node:fs'),path=require('node:path'); const dir=path.join(process.argv[process.argv.indexOf('--outdir')+1],'artifact');fs.mkdirSync(dir,{recursive:true});fs.writeFileSync(path.join(dir,'fused-access.json'),'{"services":{"other":{}}}');`)
	// A compiler that emits permissions for another service cannot publish an artifact.
	if _, err := build(req); err == nil || !strings.Contains(err.Error(), "Compiled Fused permissions differ") {
		t.Fatal(err)
	}
}

// requireNode24 skips only the test exercising the framework's actual Node runtime.
func requireNode24(t *testing.T) {
	t.Helper()
	node, err := exec.Command("node", "--version").Output()
	// Only the Fused analyzer assertion requires Node on PATH.
	if err != nil {
		t.Skip("requires Node 24+")
	}
	major, err := strconv.Atoi(strings.Split(strings.TrimPrefix(strings.TrimSpace(string(node)), "v"), ".")[0])
	// The embedded type-stripped selection rules require the framework's Node baseline.
	if err != nil || major < 24 {
		t.Skip("requires Node 24+")
	}
}

// TestInitReactStarter checks that choosing the UI enrolls its source files and configuration together.
func TestInitReactStarter(t *testing.T) {
	t.Chdir(t.TempDir())
	result, err := initProject(packagekit.InitRequest{Version: 1, DefaultName: "sample", UI: true})
	// The runtime needs the server-side loader as well as the React entry.
	if err != nil || result.Files["load.ts"] == "" || result.Files["ui.tsx"] == "" {
		t.Fatal("missing React starter", err)
	}
	var app struct{ UI struct{ Entry, Load string } }
	// The scaffolded config must point at the files offered by the package.
	if err = json.Unmarshal([]byte(result.Files["minion.json"]), &app); err != nil || app.UI.Entry != "ui.tsx" || app.UI.Load != "load.ts" {
		t.Fatal("invalid React starter config", err)
	}
}

// TestMinionPackageOnlyProject checks the project produced after adding Minion without init.
func TestMinionPackageOnlyProject(t *testing.T) {
	root := t.TempDir()
	fixture(t, filepath.Join(root, ".hostlier"), "version: 2\npackages:\n  minion: {name: fresh, entry: service.ts}\n")
	fixture(t, filepath.Join(root, "service.ts"), "export default {}")
	inputs := map[string]any{"name": "fresh", "entry": "service.ts", "baseImage": "node:24.21.0-alpine"}
	req := packagekit.ProjectPlanRequest{Version: 1, ProjectDir: root, Architecture: "arm64", Config: inputs}
	res, err := plan(req)
	// A project can build its own container without an initialized deployment or minion.json.
	if err != nil {
		t.Fatal(err)
	}
	var p model.Project
	// The plan still owns a named local artifact and public HTTP listener.
	if err = json.Unmarshal(res.Project, &p); err != nil || p.Name != "fresh" || p.Source.Container.Context != "." || p.Endpoint.Port != 8080 {
		t.Fatal(p, err)
	}
	inputs["entry"] = "missing.ts"
	// Missing explicit entries should fail before a build starts.
	if _, err = plan(req); err == nil {
		t.Fatal("missing source entry was accepted")
	}
}
