package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"hostlier/internal/model"
	"hostlier/pkg/packagekit"
)

// build materializes only a previously reviewed project into Hostlier's staging directory.
func build(req packagekit.ProjectPackageRequest) (result packagekit.ProjectPackageResponse, err error) {
	// Reject unsupported target platforms before inspecting the filesystem.
	if err := validTarget(req.Version, req.ProjectDir, req.Architecture); err != nil {
		return result, err
	}
	// The caller owns the stage and expects artifacts from this run alone.
	if err := checkEmptyStage(req.OutputDir); err != nil {
		return result, err
	}
	app, err := readApp(req.ProjectDir, req.Config)
	// A partial app configuration cannot be matched to the reviewed project.
	if err != nil {
		return result, err
	}
	route, err := reviewedRoute(req, app)
	// Only a hostname consistent with the reviewed topology may reach the compiler.
	if err != nil {
		return result, err
	}
	scope, err := consentScope(req.ProjectDir, app)
	// Permission analysis must succeed before any catalog access.
	if err != nil {
		return result, err
	}
	// Check scope before any installer or compiler receives a catalog credential.
	if !jsonEqual(scope, req.Declarations) {
		return result, errors.New("Fused declarations changed. Run plan again.")
	}
	script, err := ensureCompiler(req.ProjectDir)
	// Use the framework pinned by the project or installed by Hostlier.
	if err != nil {
		return result, err
	}
	dest := filepath.Join(req.OutputDir, "release")
	// Failed compilers can leave partial output. Do not advertise it.
	if err := compile(req, app, script, dest, route); err != nil {
		return result, err
	}
	// Return the artifact only after its emitted permissions match the review.
	if err := verifyManifest(dest, scope); err != nil {
		return result, err
	}
	result.Version = 1
	result.Artifact = packagekit.ProjectArtifact{Kind: "container-context", Path: "release/artifact"}
	return result, nil
}

// verifyManifest rejects compiler output whose actual access exceeds or differs from the plan.
func verifyManifest(dest string, scope json.RawMessage) error {
	// Projects requesting no new grant have no reviewed manifest to verify.
	if scope == nil {
		return nil
	}
	var declared declarations
	// Decode the same declarations object supplied to the CLI at plan time.
	if err := json.Unmarshal(scope, &declared); err != nil {
		return err
	}
	actual, err := readScope(filepath.Join(dest, "artifact", "fused-access.json"))
	// A build requesting consent must emit its actual runtime manifest.
	if err != nil {
		return err
	}
	// Compare the complete manifest, including buckets and provider versions.
	if !jsonEqual(actual, declared.Manifest) {
		return errors.New("Compiled Fused permissions differ from the reviewed declarations. Run plan again.")
	}
	return nil
}

// checkEmptyStage rejects symlink destinations and pre-populated output directories.
func checkEmptyStage(path string) error {
	// Resolve output independently of the compiler's working directory.
	if !filepath.IsAbs(path) {
		return errors.New("An absolute output directory is required")
	}
	info, err := os.Lstat(path)
	// Lstat rejects symlinks even when they point to a directory.
	if err != nil || !info.IsDir() {
		return errors.New("Minion package requires an owned output directory")
	}
	entries, err := os.ReadDir(path)
	// Old output could mix into the new release.
	if err != nil || len(entries) > 0 {
		return errors.New("Minion package requires an empty output directory")
	}
	return nil
}

// reviewedRoute verifies the authored deployment topology before using its CLI-selected hostname.
func reviewedRoute(req packagekit.ProjectPackageRequest, app projectFile) (string, error) {
	current, err := effectiveProject(req.ProjectDir, app, req.Architecture)
	// Rebuild the effective project from current inputs to detect drift.
	if err != nil {
		return "", err
	}
	var reviewed model.Project
	// The CLI must supply a complete reviewed project object.
	if json.Unmarshal(req.Project, &reviewed) != nil {
		return "", errors.New("Invalid reviewed Minion project")
	}
	// Reject topology changes before accepting the selected hostname.
	if err := validateReviewed(current, reviewed); err != nil {
		return "", err
	}
	return primary(reviewed).Route, nil
}

// findCompiler follows npm's ancestor resolution for standalone and workspace installations.
func findCompiler(root string) string {
	// Search parent node_modules directories to support hoisted workspaces.
	for dir := root; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, "node_modules", "@fused", "minion", "dist", "src", "cli.js")
		// Prefer the nearest installed copy, following Node's lookup order.
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
		// Stop when the filesystem root is its own parent.
		if dir == filepath.Dir(dir) {
			return ""
		}
	}
}

// ensureCompiler installs from the bundled framework only if the project cannot resolve its own compiler.
func ensureCompiler(root string) (string, error) {
	// Keep an already-installed compiler at the project's chosen version.
	if script := findCompiler(root); script != "" {
		return script, nil
	}
	cli := os.Getenv("HOSTLIER_CLI")
	// The runner supplies the absolute CLI path that owns the framework bundle.
	if !filepath.IsAbs(cli) {
		return "", errors.New("Install the framework with hostlier minion install")
	}
	cmd := exec.Command(cli, "minion", "install")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// The installer only needs Hostlier's package location, Node/npm, and its cache.
	cmd.Env = withoutFusedCredentials()
	// Avoid returning partial installations as valid artifacts.
	if err := cmd.Run(); err != nil {
		return "", errors.New("Could not install the Minion framework; run hostlier minion install for details")
	}
	// Verify the installation actually contains the expected build entry point.
	if script := findCompiler(root); script != "" {
		return script, nil
	}
	return "", errors.New("Installed Minion framework has no compiler")
}

// compile adapts the reviewed project to the protocol supported by its pinned compiler.
func compile(req packagekit.ProjectPackageRequest, app projectFile, script, dest, route string) error {
	protocol, err := compilerProtocol(script)
	// Read the pinned compiler's contract before passing any build options.
	if err != nil {
		return err
	}
	// The final argument is filled with the resolved config snapshot below.
	args := []string{script, "build", ""}
	// Select the invocation supported by this installation.
	switch protocol {
	// Current compilers accept deployment inputs independently of minion.json.
	case 1:
		args = append(args, "--outdir", dest, "--architecture", req.Architecture)
		// An absent hostname leaves the compiler's route default intact.
		if route != "" {
			args = append(args, "--route", route)
		}
	// Unversioned compilers only accept their deployment inputs in JSON.
	case 0:
		// Catch missing mandatory settings before starting the legacy compiler.
		if err := applyLegacyTarget(app, req.Architecture, dest, route); err != nil {
			return err
		}
	// New compiler protocols require an explicit adapter.
	default:
		return errors.New("Unsupported Minion compiler protocol; update the Hostlier CLI")
	}
	// Give every compiler the same resolved inputs, including apps authored only in .hostlier.
	name, err := stageConfig(req.ProjectDir, app)
	// Do not fall back to mutable inputs if snapshotting fails.
	if err != nil {
		return err
	}
	defer os.Remove(name)
	args[2] = name
	cmd := exec.Command("node", args...)
	cmd.Dir = req.ProjectDir
	cmd.Stderr = os.Stderr
	// Report the failed build without copying compiler output into the public result.
	if err := cmd.Run(); err != nil {
		return errors.New("Minion build failed; run hostlier minion build for compiler diagnostics")
	}
	return nil
}

// stageConfig puts the snapshot beside the authored config so relative source imports still resolve.
func stageConfig(root string, app projectFile) (string, error) {
	tmp, err := os.CreateTemp(root, ".hostlier-minion-hook-*.json")
	// Each concurrent build needs its own private config file.
	if err != nil {
		return "", errors.New("Cannot stage the reviewed compiler config")
	}
	writeErr := json.NewEncoder(tmp).Encode(app.raw)
	closeErr := tmp.Close()
	// Remove incomplete snapshots even when the error occurs during close.
	if writeErr != nil || closeErr != nil {
		os.Remove(tmp.Name())
		return "", errors.New("Cannot stage the reviewed compiler config")
	}
	return tmp.Name(), nil
}

// applyLegacyTarget supplies fields that pre-protocol compilers require in their config.
func applyLegacyTarget(app projectFile, arch, dest, route string) error {
	// The legacy compiler derives its public origin from this hostname.
	if route == "" {
		return errors.New("This pinned Minion compiler needs a deployment hostname")
	}
	// Old compilers read a single config. Preserve every extension and resolve source paths beside it.
	for key, value := range map[string]string{"outdir": dest, "architecture": arch, "route": route} {
		app.raw[key], _ = json.Marshal(value)
	}
	// Provide image defaults required by pre-protocol versions.
	for key, value := range map[string]string{"image": app.Name + ":0.1.0", "baseImage": "node:24.21.0-bookworm-slim"} {
		// Keep image and baseImage values explicitly authored by the project.
		if _, ok := app.raw[key]; !ok {
			app.raw[key], _ = json.Marshal(value)
		}
	}
	return nil
}

// withoutFusedCredentials prevents non-catalog stages from inheriting Fused API credentials.
func withoutFusedCredentials() []string {
	env := []string{}
	// Start from the hook runner's restricted environment.
	for _, value := range os.Environ() {
		// Only the catalog-aware build step needs Fused variables.
		if !strings.HasPrefix(value, "FUSED_") {
			env = append(env, value)
		}
	}
	return env
}

// compilerProtocol reads the package manifest so pinned projects keep their original compiler contract.
func compilerProtocol(script string) (int, error) {
	var metadata struct {
		Hostlier struct{ MinionBuildProtocol int }
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(script), "..", "..", "package.json"))
	// Distinguish an unreadable manifest from a valid older manifest without a protocol field.
	if err != nil || json.Unmarshal(raw, &metadata) != nil {
		return 0, errors.New("Invalid installed Minion compiler manifest")
	}
	return metadata.Hostlier.MinionBuildProtocol, nil
}
