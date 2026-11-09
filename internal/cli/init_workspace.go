package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const initWorkspaceLimit = 512
const initWorkspaceEntryLimit = 20000
const initWorkspaceManifestLimit = 1024 * 1024

// initWorkspacePackage retains only discovery metadata, never environment values or executable hooks.
type initWorkspacePackage struct {
	Name                 string            `json:"name"`
	PackageManager       string            `json:"packageManager"`
	Scripts              map[string]string `json:"scripts"`
	Workspaces           json.RawMessage   `json:"workspaces"`
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

// initWorkspaceScan bounds traversal and owns the normalized include/exclude patterns.
type initWorkspaceScan struct {
	root        string
	patterns    []string
	directories map[string]bool
	entries     int
}

// detectWorkspaceApps discovers every deployable workspace without selecting or executing an app.
func detectWorkspaceApps(root, architecture string) (map[string]initApp, error) {
	root, err := filepath.Abs(root)
	// Canonical roots make every containment comparison independent of the invocation directory.
	if err != nil {
		return nil, err
	}
	patterns, err := initWorkspacePatterns(root)
	// A non-workspace project belongs to the single-app detector.
	if err != nil || patterns == nil {
		return nil, err
	}
	scanner := initWorkspaceScan{root: root, patterns: patterns, directories: map[string]bool{".": true}}
	// Only paths potentially covered by a workspace pattern are traversed.
	if err := scanner.walk(".", 0); err != nil {
		return nil, err
	}
	packages, err := scanner.packages()
	// Duplicate names and unreadable manifests must not produce an arbitrary dependency graph.
	if err != nil {
		return nil, err
	}
	apps, err := detectInitWorkspaceCandidates(root, architecture, packages)
	// Framework-specific packaging is validated before dependency choices are offered.
	if err != nil {
		return nil, err
	}
	// Runtime graph construction never includes development-only dependencies.
	if err := initWorkspaceDependencies(root, apps, packages); err != nil {
		return nil, err
	}
	return apps, nil
}

// detectInitWorkspaceCandidates preserves all runnable apps while leaving libraries as graph metadata.
func detectInitWorkspaceCandidates(root, architecture string, packages map[string]initWorkspacePackage) (map[string]initApp, error) {
	apps := map[string]initApp{}
	// Sorted discovery keeps errors and previews deterministic across filesystems.
	for _, relative := range initWorkspaceSortedKeys(packages) {
		candidate, err := initWorkspaceRunnable(root, relative, packages[relative])
		// Invalid app markers require repair instead of falling back to another workspace.
		if err != nil {
			return nil, err
		}
		// Libraries can participate in dependency tracing but are never deployment candidates.
		if !candidate {
			continue
		}
		app, err := detectSingleInitApp(filepath.Join(root, filepath.FromSlash(relative)), architecture)
		// The parent detector owns framework-specific project and artifact generation.
		if err != nil {
			return nil, fmt.Errorf("workspace %s: %w", relative, err)
		}
		app, err = prepareInitWorkspaceApp(root, relative, app, packages["."])
		// Packaging errors require an authored recipe, not a broken child context.
		if err != nil {
			return nil, err
		}
		apps[relative] = app
	}
	return apps, nil
}

// initWorkspacePatterns merges declared npm/Yarn and pnpm workspace membership without guessing defaults.
func initWorkspacePatterns(root string) ([]string, error) {
	manifest, _, err := readInitWorkspacePackage(root)
	// Malformed metadata must not silently disable workspace detection.
	if err != nil {
		return nil, err
	}
	patterns, err := parseInitNPMWorkspaces(manifest.Workspaces)
	// An invalid workspace shape is actionable even when another package manager file exists.
	if err != nil {
		return nil, err
	}
	data, exists, err := readInitWorkspaceFile(filepath.Join(root, "pnpm-workspace.yaml"))
	// Both manifests are bounded and must be ordinary local files.
	if err != nil {
		return nil, err
	}
	// A pnpm workspace can exist without package.json workspaces.
	if exists {
		var pnpm struct {
			Packages []string `yaml:"packages"`
		}
		// YAML decoding reads configuration only; package hooks are never invoked.
		if err := yaml.Unmarshal(data, &pnpm); err != nil {
			return nil, fmt.Errorf("invalid pnpm workspace: %w", err)
		}
		patterns = append(patterns, pnpm.Packages...)
		// An explicit empty workspace remains distinct from no workspace declaration.
		if patterns == nil {
			patterns = []string{}
		}
	}
	return validateInitWorkspacePatterns(patterns)
}

// parseInitNPMWorkspaces supports npm arrays and Yarn's packages object shape.
func parseInitNPMWorkspaces(raw json.RawMessage) ([]string, error) {
	// Missing or null configuration does not declare a workspace.
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var patterns []string
	// The common npm workspace form is an array of package directory globs.
	if err := json.Unmarshal(raw, &patterns); err == nil {
		return patterns, nil
	}
	var object struct {
		Packages []string `json:"packages"`
	}
	// A package object must actually provide its membership list.
	if err := json.Unmarshal(raw, &object); err != nil || object.Packages == nil {
		return nil, fmt.Errorf("package.json workspaces must be an array or an object with packages")
	}
	return object.Packages, nil
}

// validateInitWorkspacePatterns rejects path escapes and unsupported syntax before touching directories.
func validateInitWorkspacePatterns(patterns []string) ([]string, error) {
	// Bound pattern work independently from filesystem size.
	if len(patterns) > 128 {
		return nil, fmt.Errorf("workspace discovery supports at most 128 patterns")
	}
	// Require normalized relative paths so different spellings cannot disguise membership.
	for _, pattern := range patterns {
		// Validate one bounded pattern before filesystem traversal starts.
		if err := validateInitWorkspacePattern(pattern); err != nil {
			return nil, err
		}
	}
	return patterns, nil
}

// validateInitWorkspacePattern permits safe relative glob segments without shell expansion.
func validateInitWorkspacePattern(pattern string) error {
	value := strings.TrimPrefix(pattern, "!")
	// Shell expansion and platform-specific path separators are never interpreted.
	if value == "" || len(value) > 1024 || path.IsAbs(value) || strings.ContainsAny(value, "\\:{}") {
		return fmt.Errorf("unsupported workspace pattern %q; use relative directory globs", pattern)
	}
	// Reject traversal, duplicate separators and redundant path segments.
	if value != path.Clean(value) || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("workspace pattern escapes or ambiguously names its root: %q", pattern)
	}
	return validateInitWorkspaceSegments(strings.Split(value, "/"))
}

// validateInitWorkspaceSegments bounds matcher state and validates ordinary glob expressions.
func validateInitWorkspaceSegments(parts []string) error {
	// Patterns cannot amplify a shallow scan into excessive matcher recursion.
	if len(parts) > 32 {
		return fmt.Errorf("workspace pattern exceeds 32 path segments")
	}
	// Every segment uses the same path matcher as discovery.
	for _, part := range parts {
		// Any parent traversal remains unsafe even if path.Clean would collapse it.
		if part == ".." {
			return fmt.Errorf("workspace pattern contains parent traversal")
		}
		// Double-star has directory semantics; other segments use Go's glob syntax.
		if _, err := path.Match(part, "probe"); err != nil {
			return fmt.Errorf("invalid workspace glob: %w", err)
		}
	}
	return nil
}

// readInitWorkspaceFile reads only named metadata with a strict size and regular-file boundary.
func readInitWorkspaceFile(filename string) ([]byte, bool, error) {
	info, err := os.Lstat(filename)
	// Missing optional metadata is ordinary single-app or Docker/static workspace behavior.
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	// Metadata errors must remain visible instead of broadening discovery.
	if err != nil {
		return nil, false, err
	}
	// Do not follow a metadata symlink into a private file or another checkout.
	if !info.Mode().IsRegular() || info.Size() > initWorkspaceManifestLimit {
		return nil, false, fmt.Errorf("workspace metadata must be a regular file of at most 1 MiB: %s", filename)
	}
	file, err := os.Open(filename)
	// LimitReader also bounds files that grow after the stat.
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, initWorkspaceManifestLimit+1))
	// Reject a concurrent oversized rewrite rather than parsing truncated configuration.
	if len(data) > initWorkspaceManifestLimit {
		return nil, false, fmt.Errorf("workspace metadata exceeds 1 MiB: %s", filename)
	}
	return data, true, err
}

// readInitWorkspacePackage extracts package metadata without reading credentials or executing scripts.
func readInitWorkspacePackage(directory string) (initWorkspacePackage, bool, error) {
	var manifest initWorkspacePackage
	data, exists, err := readInitWorkspaceFile(filepath.Join(directory, "package.json"))
	// Docker and static apps need not have an npm manifest.
	if err != nil || !exists {
		return manifest, exists, err
	}
	// Invalid package metadata makes runtime dependency inference unsafe.
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, true, fmt.Errorf("invalid package.json in %s: %w", directory, err)
	}
	return manifest, true, nil
}

// walk scans only potentially included directories, bounding both depth and directory entries.
func (scan *initWorkspaceScan) walk(relative string, depth int) error {
	// Recursive globs cannot cause unbounded filesystem traversal.
	if depth > 16 {
		return fmt.Errorf("workspace discovery exceeded 16 directory levels")
	}
	directory, err := os.Open(filepath.Join(scan.root, filepath.FromSlash(relative)))
	// Directory permissions are part of the user's discovery input.
	if err != nil {
		return err
	}
	defer directory.Close()
	// Read small batches rather than loading an unbounded directory listing.
	for {
		entries, err := directory.ReadDir(256)
		// An EOF batch may still contain entries and is processed before returning.
		for _, entry := range entries {
			scan.entries++
			// Count files too, so enormous unrelated directory listings are bounded.
			if scan.entries > initWorkspaceEntryLimit {
				return fmt.Errorf("workspace discovery exceeded %d directory entries", initWorkspaceEntryLimit)
			}
			// Entry handling owns symlink and candidate checks.
			if err := scan.visit(path.Join(relative, entry.Name()), entry, depth); err != nil {
				return err
			}
		}
		// A complete directory ends traversal normally.
		if err == io.EOF {
			return nil
		}
		// Other enumeration errors must not silently remove app candidates.
		if err != nil {
			return err
		}
	}
}

// visit excludes generated directories and rejects symlinked candidate paths before recursion.
func (scan *initWorkspaceScan) visit(relative string, entry os.DirEntry, depth int) error {
	// These directories contain dependencies or generated state, never authored workspaces.
	if initWorkspaceIgnored(entry.Name()) {
		return nil
	}
	included, descend := scan.matches(relative)
	// Paths outside declared membership do not need inspection.
	if !included && !descend {
		return nil
	}
	// A workspace symlink can escape or alias another candidate even when its target is internal.
	if entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink workspace paths are ambiguous or unsafe: %s", relative)
	}
	// Ordinary files cannot be workspace directories.
	if !entry.IsDir() {
		return nil
	}
	// Overlapping patterns refer to one candidate, not multiple selectable apps.
	if included {
		scan.directories[relative] = true
		// Bound packages separately from directory entries.
		if len(scan.directories) > initWorkspaceLimit {
			return fmt.Errorf("workspace discovery exceeded %d candidate directories", initWorkspaceLimit)
		}
	}
	// Literal and single-star terminal patterns do not scan inside selected packages.
	if descend {
		return scan.walk(relative, depth+1)
	}
	return nil
}

// initWorkspaceIgnored avoids dependency trees and private/generated application state.
func initWorkspaceIgnored(name string) bool {
	switch name { // These standard metadata directories are not deployment candidates.
	case "node_modules", ".git", ".minion", ".next", ".turbo":
		return true // Skip whole generated trees.
	}
	return false
}

// matches applies exclusions after inclusions and reports whether a descendant can still match.
func (scan *initWorkspaceScan) matches(relative string) (bool, bool) {
	included, descend := false, false
	// Compare slash-separated package paths consistently across platforms.
	for _, pattern := range scan.patterns {
		value := strings.TrimPrefix(pattern, "!")
		matched := initWorkspaceGlob(strings.Split(value, "/"), strings.Split(relative, "/"), false)
		// Exclusions win independently of their order in either workspace manifest.
		if strings.HasPrefix(pattern, "!") {
			// Excluded subtrees need no traversal.
			if matched {
				return false, false
			}
			continue
		}
		included = included || matched
		descend = descend || initWorkspaceGlob(strings.Split(value, "/"), strings.Split(relative, "/"), true)
	}
	return included, descend
}

// initWorkspaceGlob implements segment wildcards and recursive directories without shell expansion.
func initWorkspaceGlob(pattern, relative []string, prefix bool) bool {
	return memoInitWorkspaceGlob(pattern, relative, prefix, map[[2]int]bool{}, map[[2]int]bool{})
}

// memoInitWorkspaceGlob caches each segment state to bound repeated recursive wildcard patterns.
func memoInitWorkspaceGlob(pattern, relative []string, prefix bool, known, values map[[2]int]bool) bool {
	key := [2]int{len(pattern), len(relative)}
	// Each pattern/path pair has one result for this invocation's fixed prefix mode.
	if known[key] {
		return values[key]
	}
	result := evaluateInitWorkspaceGlob(pattern, relative, prefix, known, values)
	known[key], values[key] = true, result
	return result
}

// evaluateInitWorkspaceGlob consumes directory segments without inspecting filesystem targets.
func evaluateInitWorkspaceGlob(pattern, relative []string, prefix bool, known, values map[[2]int]bool) bool {
	// Prefix mode asks whether another directory can still complete the pattern.
	if len(relative) == 0 {
		return finishInitWorkspaceGlob(pattern, prefix)
	}
	// A consumed pattern cannot match another directory segment.
	if len(pattern) == 0 {
		return false
	}
	// Double-star may consume no directory or one directory and keep matching.
	if pattern[0] == "**" {
		return memoInitWorkspaceGlob(pattern[1:], relative, prefix, known, values) || memoInitWorkspaceGlob(pattern, relative[1:], prefix, known, values)
	}
	matched, _ := path.Match(pattern[0], relative[0])
	return matched && memoInitWorkspaceGlob(pattern[1:], relative[1:], prefix, known, values)
}

// finishInitWorkspaceGlob handles a fully consumed path without exponential trailing-wildcard recursion.
func finishInitWorkspaceGlob(pattern []string, prefix bool) bool {
	// Prefix matching needs a remaining segment to reach a future descendant.
	if prefix {
		return len(pattern) > 0
	}
	// A complete match permits only zero-length recursive wildcards at the end.
	for _, part := range pattern {
		// Any ordinary segment still needs a directory.
		if part != "**" {
			return false
		}
	}
	return true
}

// packages reads every selected package so library names remain available for dependency tracing.
func (scan *initWorkspaceScan) packages() (map[string]initWorkspacePackage, error) {
	packages := map[string]initWorkspacePackage{}
	names := map[string]string{}
	// Stable path order makes duplicate-name errors reproducible.
	for _, relative := range initWorkspaceSortedKeys(scan.directories) {
		manifest, _, err := readInitWorkspacePackage(filepath.Join(scan.root, filepath.FromSlash(relative)))
		// Any malformed workspace manifest requires repair before selection.
		if err != nil {
			return nil, err
		}
		// Named libraries and apps share one package namespace.
		if previous, exists := names[manifest.Name]; exists && manifest.Name != "" {
			return nil, fmt.Errorf("duplicate workspace package name %q in %s and %s", manifest.Name, previous, relative)
		}
		names[manifest.Name] = relative
		packages[relative] = manifest
	}
	return packages, nil
}

// initWorkspaceRunnable separates deployment entrypoints from library-only package directories.
func initWorkspaceRunnable(root, relative string, manifest initWorkspacePackage) (bool, error) {
	// A declared start command is inspected, never executed, during app detection.
	if strings.TrimSpace(manifest.Scripts["start"]) != "" {
		return true, nil
	}
	// Docker, Minion and static entrypoints also make a workspace independently runnable.
	for _, marker := range []string{"Dockerfile", "minion.json", "index.html"} {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative), marker))
		// Missing alternatives leave room for another supported framework marker.
		if os.IsNotExist(err) {
			continue
		}
		// Unsafe markers must not pass through to the single-app detector.
		if err != nil {
			return false, err
		}
		// Do not follow app entrypoint symlinks during detection.
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("workspace app marker must be a regular file: %s/%s", relative, marker)
		}
		return true, nil
	}
	return false, nil
}

// initWorkspaceSortedKeys keeps all candidate, dependency and diagnostic output deterministic.
func initWorkspaceSortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	// Maps deliberately do not encode discovery order.
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// initWorkspaceDependencies collapses runtime library edges into explicit deployable-app dependencies.
func initWorkspaceDependencies(root string, apps map[string]initApp, packages map[string]initWorkspacePackage) error {
	names := map[string]string{}
	// Only declared workspace names can imply another local runtime component.
	for relative, manifest := range packages {
		// Anonymous packages are reachable only by local paths.
		if manifest.Name != "" {
			names[manifest.Name] = relative
		}
	}
	// Each app gets an independently bounded traversal through library packages.
	for _, relative := range initWorkspaceSortedKeys(apps) {
		dependencies, notes, err := traceInitWorkspaceDependencies(root, relative, apps, packages, names)
		// Unsafe explicit local references need repair before a deployment choice.
		if err != nil {
			return err
		}
		app := apps[relative]
		app.Dependencies = dependencies
		app.Notes = append(app.Notes, notes...)
		apps[relative] = app
	}
	return nil
}

// traceInitWorkspaceDependencies follows libraries without choosing how their runtime apps should be deployed.
func traceInitWorkspaceDependencies(root, owner string, apps map[string]initApp, packages map[string]initWorkspacePackage, names map[string]string) ([]string, []string, error) {
	queue, visited := []string{owner}, map[string]bool{owner: true}
	dependencies, notes := map[string]bool{}, map[string]bool{}
	// There are at most initWorkspaceLimit packages, and each is visited at most once.
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		edges, warnings, err := initWorkspaceEdges(root, current, packages[current], packages, names)
		// Path errors cannot be converted into absent runtime dependencies.
		if err != nil {
			return nil, nil, err
		}
		// Preserve one copy of each unresolved-runtime diagnostic.
		for _, warning := range warnings {
			notes[warning] = true
		}
		// A deployable dependency is exposed to the caller; only library edges are collapsed.
		for _, target := range edges {
			// Cycles and duplicate optional dependencies terminate here.
			if visited[target] {
				continue
			}
			visited[target] = true
			// The caller decides skip/app/sidecar ownership.
			if _, deployable := apps[target]; deployable {
				dependencies[target] = true
				continue
			}
			queue = append(queue, target)
		}
	}
	return initWorkspaceSortedKeys(dependencies), initWorkspaceSortedKeys(notes), nil
}

// initWorkspaceEdges reads only runtime dependency sections; development tools never become services.
func initWorkspaceEdges(root, relative string, manifest initWorkspacePackage, packages map[string]initWorkspacePackage, names map[string]string) ([]string, []string, error) {
	runtime := map[string]string{}
	// Optional dependencies override dependencies exactly as npm package metadata specifies.
	for name, value := range manifest.Dependencies {
		runtime[name] = value
	}
	// An optional local app still requires an explicit user decision, never automatic deployment.
	for name, value := range manifest.OptionalDependencies {
		runtime[name] = value
	}
	targets, notes := map[string]bool{}, []string{}
	// Stable ordering ensures repeatable graph and warning output.
	for _, name := range initWorkspaceSortedKeys(runtime) {
		target, warning, err := resolveInitWorkspaceDependency(root, relative, name, runtime[name], packages, names)
		// Explicit path escapes cannot become registry fallbacks.
		if err != nil {
			return nil, nil, err
		}
		// A known workspace is a local runtime dependency.
		if target != "" {
			targets[target] = true
		}
		// Unresolved local references stay visible for review.
		if warning != "" {
			notes = append(notes, warning)
		}
	}
	return initWorkspaceSortedKeys(targets), notes, nil
}

// resolveInitWorkspaceDependency handles named workspace packages and explicit file/link aliases.
func resolveInitWorkspaceDependency(root, relative, name, version string, packages map[string]initWorkspacePackage, names map[string]string) (string, string, error) {
	// Explicit filesystem dependencies must be contained before matching package membership.
	if strings.HasPrefix(version, "file:") || strings.HasPrefix(version, "link:") {
		value := strings.SplitN(version, ":", 2)[1]
		target, err := initWorkspaceDependencyPath(root, relative, value)
		// A path escape cannot be an inferred sidecar.
		if err != nil {
			return "", "", err
		}
		// Aliases can use a dependency key different from package.name.
		if _, exists := packages[target]; exists {
			return target, "", nil
		}
		return "", fmt.Sprintf("Runtime dependency %s in %s points outside declared workspace membership; review it manually.", name, relative), nil
	}
	// npm aliases explicitly name a registry package rather than the local dependency key.
	if strings.HasPrefix(version, "npm:") {
		return "", "", nil
	}
	// A local package name is a candidate relationship, not proof it must run as a service.
	if target, exists := names[name]; exists {
		return target, "", nil
	}
	// Missing workspace protocol targets cannot be silently mistaken for remote dependencies.
	if strings.HasPrefix(version, "workspace:") {
		return "", fmt.Sprintf("Runtime workspace dependency %s in %s has no matching package; review its alias or workspace patterns.", name, relative), nil
	}
	return "", "", nil
}

// initWorkspaceDependencyPath refuses escapes and symlink aliases in explicit runtime file dependencies.
func initWorkspaceDependencyPath(root, relative, value string) (string, error) {
	// Local runtime references must not contain platform-specific separators or URLs.
	if value == "" || filepath.IsAbs(value) || strings.ContainsAny(value, "\\:") {
		return "", fmt.Errorf("unsafe local runtime dependency path in %s", relative)
	}
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative), filepath.FromSlash(value)))
	candidate, err := filepath.Rel(root, target)
	// Lexical containment is checked before resolving any filesystem targets.
	if err != nil || candidate == ".." || strings.HasPrefix(candidate, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("local runtime dependency escapes workspace root in %s", relative)
	}
	// Resolve only bounded local components after lexical containment succeeds.
	if err := validateInitWorkspaceDependencyLinks(root, candidate); err != nil {
		return "", err
	}
	return filepath.ToSlash(candidate), nil
}

// validateInitWorkspaceDependencyLinks rejects symlink aliases before package graph resolution.
func validateInitWorkspaceDependencyLinks(root, candidate string) error {
	current := root
	// Inspect every directory component so an internal-looking dependency cannot follow an external symlink.
	for _, part := range strings.Split(candidate, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		// Missing local dependencies remain review notes, not guessed apps.
		if os.IsNotExist(err) {
			return nil
		}
		// Permission failures do not weaken containment.
		if err != nil {
			return err
		}
		// Reject aliases even when their targets currently remain inside the root.
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("local runtime dependency uses a symlink")
		}
	}
	return nil
}

// prepareInitWorkspaceApp replaces generic child-only Node scaffolds with one root-context workspace recipe.
func prepareInitWorkspaceApp(root, relative string, app initApp, rootManifest initWorkspacePackage) (initApp, error) {
	// Authored Dockerfiles remain authoritative and may need an explicit shared-context override.
	if app.Kind == "docker" {
		app.Notes = append(app.Notes, "Workspace Dockerfile keeps its authored child context; use --context for shared files if required.")
		return app, nil
	}
	// Static and Minion apps own their existing packaging contracts.
	if app.Kind != "node" {
		return app, nil
	}
	// npm workspace recipes cannot safely substitute for Yarn or pnpm installations.
	if rootManifest.PackageManager != "" && !strings.HasPrefix(rootManifest.PackageManager, "npm@") {
		return app, fmt.Errorf("workspace %s uses a non-npm package manager; provide an authored Dockerfile", relative)
	}
	_, locked, err := readInitWorkspaceFile(filepath.Join(root, "package-lock.json"))
	// Workspace installation must reproduce the root lock and declared npm membership.
	if err != nil {
		return app, err
	}
	npmPatterns, _ := parseInitNPMWorkspaces(rootManifest.Workspaces)
	// Root npm metadata is required even when individual children have their own locks.
	if !locked || len(npmPatterns) == 0 {
		return app, fmt.Errorf("workspace %s needs root package.json workspaces and package-lock.json for npm packaging; provide an authored Dockerfile for pnpm/yarn", relative)
	}
	filename := path.Join(".hostlier-build", app.Name+".Dockerfile")
	app.Project.Source.Container.Context = root
	app.Project.Source.Container.Dockerfile = filename
	app.Files = map[string][]byte{filepath.Join(root, filepath.FromSlash(filename)): initWorkspaceDockerfile(relative, app.Project.Endpoint.Port, rootManifest)}
	app.Notes = initWorkspacePackagingNotes(app.Notes)
	// Root policy belongs to the user and is never replaced by a selected child's scaffold.
	if initFileExists(filepath.Join(root, ".dockerignore")) {
		app.Notes = append(app.Notes, "Review the root .dockerignore: the workspace build context contains shared packages and must exclude credentials.")
	} else { // Newly generated policy excludes private state across every workspace.
		app.Files[filepath.Join(root, ".dockerignore")] = []byte("**/node_modules\n.git\n**/.env\n**/.env.*\n**/*.pem\n**/*.key\n**/.hostlier\n**/.hostlier.*\n**/.minion\n")
	}
	app.Notes = append(app.Notes, "Workspace Node packaging uses the root npm lock, builds shared workspaces, and starts only the selected app; review the generated recipe.")
	return app, nil
}

// initWorkspaceDockerfile preserves workspace links by installing and building from the repository root.
func initWorkspaceDockerfile(relative string, port int, manifest initWorkspacePackage) []byte {
	build := "npm run build --workspaces --if-present"
	// An authored root build can express dependency ordering that generic workspace enumeration cannot infer.
	if strings.TrimSpace(manifest.Scripts["build"]) != "" {
		build = "npm run build"
	}
	command := []string{"npm", "run", "start"}
	// The root package is addressed without a workspace selector.
	if relative != "." {
		command = append(command, "--workspace", relative)
	}
	encoded, _ := json.Marshal(command)
	return []byte(fmt.Sprintf("FROM node:24.21.0-bookworm-slim\nWORKDIR /app\nCOPY . .\nRUN npm ci\nRUN %s\nENV NODE_ENV=production HOST=0.0.0.0 PORT=%d\nUSER node\nEXPOSE %d\nCMD %s\n", build, port, port, encoded))
}

// initWorkspacePackagingNotes replaces single-package lock advice with the workspace root contract.
func initWorkspacePackagingNotes(notes []string) []string {
	result := []string{}
	// Child lockfile advice is inapplicable once the root npm lock owns the installation.
	for _, note := range notes {
		// Preserve every other detector note while removing single-package lock advice.
		if strings.HasPrefix(note, "Add a package-lock.json") {
			continue
		}
		result = append(result, note)
	}
	return result
}
