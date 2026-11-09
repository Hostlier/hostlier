package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// init creates local deployment files only; server installation belongs to hostlier-controller.
func (s *session) init() error {
	// Only project-specific flags can reach discovery or write files.
	if err := s.validateInitFlags(); err != nil {
		return err
	}
	root, err := s.initDirectory()
	// All inferred source paths share the resolved project ownership boundary.
	if err != nil {
		return err
	}
	// A package initializer only runs when it was explicitly selected.
	if name, selected := s.flags["package"]; selected {
		return s.initPackage(root, name)
	}
	// A bare init needs no application type, source or installed packages.
	if !s.detectInitRequested() {
		return s.initStarter(root)
	}
	return s.initDetectedProject(root)
}

// detectInitRequested preserves explicit discovery options and keeps ordinary init independent of project contents.
func (s *session) detectInitRequested() bool {
	// Source and deployment overrides select the guided initializer.
	for _, key := range []string{"detect", "service", "compose", "dependencies", "architecture", "context", "image", "name", "port", "route"} {
		// Only flags supplied by this invocation count.
		if _, ok := s.flags[key]; ok {
			return true
		}
	}
	return false
}

// initStarter creates a v2 manifest that a builder or resource package can populate later.
func (s *session) initStarter(root string) error {
	path := filepath.Join(root, ".hostlier")
	files := map[string]string{path: "version: 2\npackages: {}\n"}
	// Match the exclusive creation policy before previewing or writing.
	if _, err := planInitSetup(root, files, nil); err != nil {
		return err
	}
	if s.flags["dry-run"] != "" { // A preview leaves even an empty project untouched.
		return s.print(map[string]any{"files": files, "dryRun": true})
	}
	// Publish through the existing exclusive scaffold writer.
	if err := writeInitSetup(root, files, nil); err != nil {
		return err
	}
	return s.print(map[string]any{"created": []string{path}})
}

// initDetectedProject derives a deployment from sources when requested by the operator.
func (s *session) initDetectedProject(root string) error {
	apps, err := s.discoverInit(root)
	// Discovery errors retain all existing files and never trigger a build.
	if err != nil {
		return err
	}
	key, selected, err := s.configureInitSelection(apps)
	// Resolve the selected app and its overrides before asking about dependency ownership.
	if err != nil {
		return err
	}
	owners, err := s.initDependencyOwners(apps, key)
	// No files are created until all dependency choices are concrete.
	if err != nil {
		return err
	}
	// Complete only included Minion apps after dependency ownership is explicit.
	if err = s.configureInitMinions(apps, owners); err != nil {
		return err
	}
	files, notes, err := assembleInit(apps, key, owners)
	// Validate the complete topology before writing any scaffold.
	if err != nil {
		return err
	}
	originals, settings := minionInitUpdates(apps, owners, files)
	// Preview only the changed public settings, never full authored Minion metadata.
	preview := map[string]any{"app": apps[key].Name, "kind": selected.Kind, "files": minionInitPreview(files, originals), "settings": settings, "notes": notes, "dryRun": s.flags["dry-run"] != ""}
	// Previews never create Dockerfiles or deployment files.
	if s.flags["dry-run"] != "" {
		return s.print(preview)
	}
	// Exclusive writes preserve user-owned files.
	if err = writeInitSetup(root, files, originals); err != nil {
		return err
	}
	return s.print(map[string]any{"app": apps[key].Name, "kind": selected.Kind, "created": minionInitCreated(files, originals), "updated": sortedInitKeys(originals), "settings": settings, "notes": notes})
}

// initDirectory resolves the chosen app root while permitting ordinary symlinked system directories.
func (s *session) initDirectory() (string, error) {
	root := s.flags["directory"]
	// Relative selections belong to the invocation directory rather than the embedding process's cwd.
	if !filepath.IsAbs(root) {
		root = filepath.Join(s.cwd, root)
	}
	root, err := filepath.Abs(root)
	// No discovery can proceed without an absolute filesystem identity.
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	// The initializer never invents a missing application directory.
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("App directory does not exist")
	}
	return filepath.EvalSymlinks(root)
}

// configureInitSelection applies explicit app identity and endpoint overrides to exactly one candidate.
func (s *session) configureInitSelection(apps map[string]initApp) (string, initApp, error) {
	key, err := s.selectInitApp(apps)
	// Ambiguous discovery never silently selects a deployment target.
	if err != nil {
		return "", initApp{}, err
	}
	selected := apps[key]
	// An explicit name affects only the selected application's identity.
	if s.flags["name"] != "" {
		selected.Name = s.flags["name"]
		selected.Project.Name = selected.Name
	}
	// Validate endpoint overrides before topology choices or output creation.
	if err = s.applyInitEndpoint(&selected); err != nil {
		return "", initApp{}, err
	}
	// The explicit deployment target also governs Minion artifact packaging.
	if selected.Minion != nil && s.flags["architecture"] != "" {
		selected.Project.Source.Container.Architecture = s.flags["architecture"]
	}
	apps[key] = selected
	return key, selected, nil
}

// validateInitFlags rejects obsolete server options before any project writes.
func (s *session) validateInitFlags() error {
	allowed := map[string]bool{"detect": true, "package": true, "directory": true, "service": true, "compose": true, "dependencies": true, "architecture": true, "context": true, "image": true, "name": true, "port": true, "route": true, "dry-run": true, "json": true, "toon": true}
	// Detect and package setup have separate option sets.
	if err := s.packageInitFlags(allowed); err != nil {
		return err
	}
	// Init accepts named selections only; extra positionals cannot change its target.
	if len(s.parsed.args) != 1 {
		return model.Err("ARGUMENTS", "Use hostlier init [--directory DIR]; server setup is hostlier-controller init")
	}
	for key := range s.flags { // Restrict this local action independently of the shared parser.
		// Old --domain must never install a server through the CLI.
		if !allowed[key] {
			return model.Err("ARGUMENTS", "Unsupported project init option --"+key+"; server setup is hostlier-controller init")
		}
	}
	architecture := defaultValue(s.flags["architecture"], "amd64")
	// Target server architecture is explicit, not the laptop's CPU.
	if architecture != "amd64" && architecture != "arm64" {
		return model.Err("ARGUMENTS", "Architecture must be amd64 or arm64")
	}
	// Competing source contracts cannot be guessed.
	if s.flags["image"] != "" && (s.flags["context"] != "" || s.flags["compose"] != "") {
		return model.Err("ARGUMENTS", "Choose image, context, or Compose input")
	}
	return nil
}

// packageInitFlags admits only options understood by explicit initializers.
func (s *session) packageInitFlags(allowed map[string]bool) error {
	// Extra package-specific options require an explicit package.
	if _, selected := s.flags["package"]; selected {
		allowed["state-dir"] = true
		allowed["ui"] = true
		// Source discovery cannot run alongside a package initializer.
		for _, key := range []string{"detect", "compose", "context", "image", "service", "dependencies"} {
			// Fail before either mode can create files.
			if s.flags[key] != "" {
				return fmt.Errorf("--package cannot be combined with --%s; use --directory to select the app", key)
			}
		}
	}
	return nil
}

// discoverInit gives explicit input selections precedence over local metadata discovery.
func (s *session) discoverInit(root string) (map[string]initApp, error) {
	architecture := defaultValue(s.flags["architecture"], "amd64")
	info, err := os.Stat(root)
	// Never create a guessed application directory.
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("App directory does not exist")
	}
	if image := s.flags["image"]; image != "" { // An explicit image needs no source-code inference.
		app := newInitApp(root, "image", architecture)
		app.Project.Source = &model.Source{Image: image}
		return map[string]initApp{".": app}, nil
	}
	if context := s.flags["context"]; context != "" { // Explicit build context bypasses workspace ambiguity.
		// Relative inputs belong to the chosen app root.
		if !filepath.IsAbs(context) {
			context = filepath.Join(root, context)
		}
		app, err := detectDockerInit(root, context, architecture)
		return map[string]initApp{".": app}, err
	}
	compose, err := initComposePath(root, s.flags["compose"])
	// Multiple Compose files require an explicit selection.
	if err != nil {
		return nil, err
	}
	// Compose declares runtime dependencies explicitly.
	if compose != "" {
		return detectComposeApps(root, compose, architecture)
	}
	apps, err := detectWorkspaceApps(root, architecture)
	// Workspaces must choose a service rather than treating the root as all apps.
	if err != nil || len(apps) > 0 {
		return apps, err
	}
	app, err := detectSingleInitApp(root, architecture)
	return map[string]initApp{".": app}, err
}

// initComposePath refuses to invent merge order for multiple Compose configuration files.
func initComposePath(root, explicit string) (string, error) {
	if explicit != "" { // Explicit filenames are relative to the chosen project root.
		// Absolute authored selections retain their identity.
		if !filepath.IsAbs(explicit) {
			explicit = filepath.Join(root, explicit)
		}
		return explicit, nil
	}
	found := []string{}
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} { // Only standard entry files participate in automatic selection.
		// Missing markers are ordinary non-Compose apps.
		if initFileExists(filepath.Join(root, name)) {
			found = append(found, filepath.Join(root, name))
		}
	}
	// Never drop a competing declared service graph.
	if len(found) > 1 {
		return "", fmt.Errorf("Multiple Compose files found; select one with --compose FILE")
	}
	if len(found) == 1 { // Docker automatically merges override files, so an importer must not silently omit them.
		for _, name := range []string{"compose.override.yaml", "compose.override.yml", "docker-compose.override.yml", "docker-compose.override.yaml"} {
			// Require explicit ownership of merge semantics.
			if initFileExists(filepath.Join(root, name)) {
				return "", fmt.Errorf("Compose override detected; provide a reviewed standalone --compose FILE")
			}
		}
		return found[0], nil
	}
	return "", nil
}

// selectInitApp asks for exactly one deployable app when discovery finds multiple candidates.
func (s *session) selectInitApp(apps map[string]initApp) (string, error) {
	keys := sortedInitKeys(apps)
	choice := s.flags["service"]
	// Library-only workspaces do not become services.
	if len(keys) == 0 {
		return "", fmt.Errorf("No deployable apps found")
	}
	// One candidate needs no redundant question.
	if choice == "" && len(keys) == 1 {
		return keys[0], nil
	}
	if choice == "" { // Headless commands must make the same explicit selection as interactive users.
		if !s.interactive {
			return "", fmt.Errorf("Select an app with --service: %s", strings.Join(keys, ", "))
		}
		var err error
		choice, err = s.ask("Which app should be initialized? ["+strings.Join(keys, ", ")+"] ", false)
		// Cancelled selection cannot choose the first app implicitly.
		if err != nil {
			return "", err
		}
	}
	choice = strings.TrimSpace(choice)
	// Exact discovery keys prevent accidental app renames.
	if _, ok := apps[choice]; !ok {
		return "", fmt.Errorf("Unknown app selection; choose one of: %s", strings.Join(keys, ", "))
	}
	return choice, nil
}

// applyInitEndpoint adjusts only explicit user selections and preserves detected service defaults.
func (s *session) applyInitEndpoint(app *initApp) error {
	if value := s.flags["port"]; value != "" { // Explicit ports override framework or Dockerfile evidence.
		port, err := strconv.Atoi(value)
		// Invalid ports cannot enter generated YAML.
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("Port must be between 1 and 65535")
		}
		app.Project.Endpoint.Port = port
		// Generated Node recipes honor the same runtime override.
		if _, ok := app.Project.Env["PORT"]; ok {
			app.Project.Env["PORT"] = value
		}
	}
	// An absent override preserves the route discovered in app metadata.
	if s.flags["route"] != "" {
		app.Project.Endpoint.Route = s.flags["route"]
	}
	if app.Kind == "minion" && s.flags["route"] != "" { // Minion's browser origin follows only an explicitly chosen public URL.
		route, err := config.ParseRoute(s.flags["route"], nil, "")
		// Reject invalid origins before serializing them.
		if err != nil {
			return err
		}
		app.Project.Env = map[string]string{"MINION_ORIGIN": route.URL}
	}
	return nil
}

// sortedInitKeys keeps prompts, generated paths and receipts stable across map iteration order.
func sortedInitKeys[V any](items map[string]V) []string {
	keys := make([]string, 0, len(items))
	// Sort keys independently of their stored values.
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// writeInitFiles writes only new files and rolls back this attempt on partial failure.
func writeInitFiles(root string, files map[string]string) error {
	keys := sortedInitKeys(files)
	for _, path := range keys { // Preflight every output before creating any of them.
		// Every generated file belongs beneath the explicitly selected project root.
		if err := validateInitOutputPath(root, path); err != nil {
			return err
		}
		// Symlinks are existing files too.
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("Refusing to overwrite existing file: %s", path)
		}
	}
	created := []string{}
	createdDirs := []string{}
	for _, path := range keys { // O_EXCL closes the race between preflight and opening each output.
		if err := ensureInitDirectory(root, filepath.Dir(path), &createdDirs); err != nil {
			rollbackInitFiles(created, createdDirs)
			return err
		}
		// Roll back both owned files and scaffold directories if exclusive creation fails.
		if err := createInitFile(path, files[path]); err != nil {
			rollbackInitFiles(created, createdDirs)
			return err
		}
		created = append(created, path)
	}
	return nil
}

// createInitFile never follows or truncates a competing output path.
func createInitFile(path, data string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	// Existing or inaccessible paths retain their original contents.
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(data)
	closeErr := f.Close()
	// Failed output must not masquerade as a completed scaffold.
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("Could not write project file %s", path)
	}
	return nil
}

// initProjectYAML keeps secrets as stored references and omits legacy zero-valued project fields.
func initProjectYAML(project model.Project, directory string) (string, error) {
	data, err := yaml.Marshal(project)
	// All output is derived from the reviewed typed model.
	if err != nil {
		return "", err
	}
	var document map[string]any
	// The typed marshal owns this intermediate document.
	if err = yaml.Unmarshal(data, &document); err != nil {
		return "", err
	}
	cleanInitDocument(document, directory, false)
	data, err = yaml.Marshal(document)
	return "# Generated by hostlier init. Review paths, ports and dependency URLs before deployment.\n" + string(data), err
}

// cleanInitDocument preserves relative artifact paths and distinguishes stored secret names from literals.
func cleanInitDocument(doc map[string]any, directory string, sidecar bool) {
	delete(doc, "image")
	delete(doc, "port")
	// Members inherit deployment identity from their parent.
	if sidecar {
		delete(doc, "name")
		delete(doc, "version")
	}
	if secrets, ok := doc["secrets"].(map[string]any); ok { // Bare internal names need explicit reference syntax in authored YAML.
		// No secret value is read or emitted by init.
		for key, value := range secrets {
			secrets[key] = "secret://" + fmt.Sprint(value)
		}
	}
	relativizeInitSource(doc, directory)
	if members, ok := doc["sidecars"].(map[string]any); ok { // Each member uses the same deployment-file root.
		// Preserve the typed member structure recursively.
		for _, member := range members {
			// Only actual member documents carry deployment fields to normalize.
			if child, ok := member.(map[string]any); ok {
				cleanInitDocument(child, directory, true)
			}
		}
	}
}

// relativizeInitSource keeps generated build contexts portable beside their deployment file.
func relativizeInitSource(doc map[string]any, directory string) {
	if source, ok := doc["source"].(map[string]any); ok { // Move filesystem identities relative to the generated config location.
		if container, ok := source["container"].(map[string]any); ok {
			// Existing relative contexts already describe portable authored paths.
			if context, ok := container["context"].(string); ok && filepath.IsAbs(context) {
				// Portable local paths survive repository moves.
				if rel, err := filepath.Rel(directory, context); err == nil {
					container["context"] = filepath.ToSlash(rel)
				}
			}
		}
	}
}

// validateInitOutputPath confines generated scaffolds before recursive parent traversal or writes.
func validateInitOutputPath(root, path string) error {
	relative, err := filepath.Rel(root, path)
	// Absolute siblings and parent traversal cannot become initializer-owned files.
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Scaffold path is outside the app directory: %s", path)
	}
	return nil
}

// ensureInitDirectory checks every parent below the selected root before creating missing scaffold directories.
func ensureInitDirectory(root, directory string, created *[]string) error {
	// The selected root was validated during discovery; system ancestors such as macOS /var may be links.
	if directory == root {
		return nil
	}
	// Existing leaf directories cannot hide a redirecting symlink in an earlier scaffold parent.
	if err := ensureInitDirectory(root, filepath.Dir(directory), created); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	// An existing parent must itself be a real directory rather than a redirecting link.
	if err == nil {
		// Lstat distinguishes directory symlinks even when their target exists.
		if !info.IsDir() {
			return fmt.Errorf("Scaffold parent is not a directory: %s", directory)
		}
		return nil
	}
	// Permission failures cannot be mistaken for an absent output directory.
	if !os.IsNotExist(err) {
		return err
	}
	// Exclusive creation never adopts a concurrent directory as this invocation's output.
	if err = os.Mkdir(directory, 0700); err != nil {
		return err
	}
	*created = append(*created, directory)
	return nil
}

// rollbackInitFiles removes only this invocation's files and now-empty directories after a failed scaffold.
func rollbackInitFiles(files, directories []string) {
	// Existing app files were never opened for writing.
	for _, path := range files {
		_ = os.Remove(path)
	}
	// Never recursively remove a directory another process has populated.
	for i := len(directories) - 1; i >= 0; i-- {
		_ = os.Remove(directories[i])
	}
}
