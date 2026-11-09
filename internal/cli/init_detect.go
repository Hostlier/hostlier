package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"hostlier/internal/model"
)

// initApp is one detected deployment candidate; discovery never runs app code or reads credentials.
type initApp struct {
	Name, Directory, Kind string
	Project               model.Project
	Files                 map[string][]byte
	Dependencies          []string
	Notes                 []string
	Minion                *minionInitSetup
}

// nodeInitManifest contains only non-secret package metadata needed for container scaffolding.
type nodeInitManifest struct {
	Name           string            `json:"name"`
	PackageManager string            `json:"packageManager"`
	Scripts        map[string]string `json:"scripts"`
	Dependencies   map[string]string `json:"dependencies"`
}

// readInitFile bounds authored metadata and rejects links before interpreting a project marker.
func readInitFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	// Missing markers are handled by the detector; other filesystem errors remain explicit.
	if err != nil {
		return nil, err
	}
	// Symlinked manifests could import another project's deployment choices or private files.
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return nil, fmt.Errorf("Project metadata must be a regular file under 1 MiB: %s", path)
	}
	f, err := os.Open(path)
	// Preserve open failures without falling back to an unrelated app detector.
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	// Bound a file that grew between inspection and reading.
	if len(data) > 1024*1024 {
		return nil, fmt.Errorf("Project metadata exceeds 1 MiB: %s", path)
	}
	return data, err
}

// initFileExists distinguishes absent markers from invalid files during actual reads.
func initFileExists(path string) bool { _, err := os.Lstat(path); return err == nil }

// newInitApp supplies an explicitly reviewable service with no guessed public domain.
func newInitApp(directory, kind, architecture string) initApp {
	name := initName(filepath.Base(directory))
	return initApp{Name: name, Directory: directory, Kind: kind, Files: map[string][]byte{}, Project: model.Project{Version: 2, Name: name, Workload: "service", Source: &model.Source{Container: &model.ContainerSource{Context: directory, Architecture: architecture}}, Endpoint: &model.Endpoint{Exposure: "public", Port: 8080}}}
}

// initName turns a package or folder name into a portable suggested app identity.
func initName(value string) string {
	value = strings.Trim(regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(value), "-"), "-")
	// Names remain bounded independently of the source package's naming conventions.
	if len(value) > 63 {
		value = strings.TrimRight(value[:63], "-")
	}
	// Empty or non-ASCII folder names need an editable fallback.
	if value == "" {
		return "app"
	}
	// Portable Hostlier identities begin with a lowercase letter.
	if value[0] < 'a' || value[0] > 'z' {
		value = "app-" + value
		// The required prefix must not exceed the deployment identity limit.
		if len(value) > 63 {
			value = value[:63]
		}
	}
	return value
}

// detectSingleInitApp prefers explicit Minion and Docker contracts over framework guesses.
func detectSingleInitApp(directory, architecture string) (initApp, error) {
	// Minion owns its build artifact and Fused identity rather than a generic Node recipe.
	if initFileExists(filepath.Join(directory, "minion.json")) {
		return detectMinionInit(directory, architecture)
	}
	// An authored Dockerfile is the authoritative packaging contract.
	if initFileExists(filepath.Join(directory, "Dockerfile")) {
		return detectDockerInit(directory, directory, architecture)
	}
	// Multiple build outputs require a user choice rather than an arbitrary directory ordering.
	contexts := []string{}
	// Inspect only standard output locations rather than traversing arbitrary source trees.
	for _, name := range []string{"dist/example", "dist/artifact", "dist", "build"} {
		// Only standard generated outputs are candidates.
		if initFileExists(filepath.Join(directory, name, "Dockerfile")) {
			contexts = append(contexts, filepath.Join(directory, name))
		}
	}
	// Old build artifacts are not implicit deployment authority.
	if len(contexts) > 1 {
		return initApp{}, fmt.Errorf("Multiple container outputs found; select one with --context DIR")
	}
	// One explicit recipe needs no generated replacement.
	if len(contexts) == 1 {
		return detectDockerInit(directory, contexts[0], architecture)
	}
	// Node and static scaffolds are generated only when their startup contract is concrete.
	if initFileExists(filepath.Join(directory, "package.json")) {
		return detectNodeInit(directory, architecture)
	}
	// Plain static sites need no build system.
	if initFileExists(filepath.Join(directory, "index.html")) {
		return detectStaticInit(directory, architecture), nil
	}
	return initApp{}, fmt.Errorf("No deployable app detected in %s; choose --directory, provide a Dockerfile, or use --image IMAGE --port PORT", directory)
}

// detectDockerInit reads literal final-stage EXPOSE ports without evaluating build instructions.
func detectDockerInit(directory, context, architecture string) (initApp, error) {
	app := newInitApp(directory, "docker", architecture)
	app.Project.Source.Container.Context = context
	data, err := readInitFile(filepath.Join(context, "Dockerfile"))
	// Invalid Dockerfiles cannot silently become guessed Node apps.
	if err != nil {
		return app, err
	}
	ports := dockerInitPorts(data)
	// A single literal listener is authoritative; ambiguity is reported in the receipt.
	if len(ports) == 1 {
		app.Project.Endpoint.Port = ports[0]
	} else {
		app.Notes = append(app.Notes, "No unique final-stage TCP EXPOSE port; review port 8080 or supply --port.")
	}
	return app, nil
}

// dockerInitPorts considers only literal TCP listeners after the final FROM instruction.
func dockerInitPorts(data []byte) []int {
	ports := []int{}
	// Only final-stage literal TCP listeners are useful app-port evidence.
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		// Comments and blank lines carry no instructions.
		if len(fields) == 0 {
			continue
		}
		// Build-stage ports do not describe the runtime image.
		if strings.EqualFold(fields[0], "FROM") {
			ports = nil
		}
		// No Dockerfile code is executed.
		if !strings.EqualFold(fields[0], "EXPOSE") {
			continue
		}
		// Docker permits multiple listener tokens on one EXPOSE instruction.
		for _, value := range fields[1:] {
			n, err := strconv.Atoi(strings.TrimSuffix(value, "/tcp"))
			// UDP and variable expressions require a manual port.
			if err == nil && n > 0 && n <= 65535 {
				ports = append(ports, n)
			}
		}
	}

	return ports
}

// detectNodeInit builds a conventional npm start app without executing package lifecycle hooks.
func detectNodeInit(directory, architecture string) (initApp, error) {
	app := newInitApp(directory, "node", architecture)
	data, err := readInitFile(filepath.Join(directory, "package.json"))
	// Malformed package metadata is reported without including its potentially private contents.
	if err != nil {
		return app, err
	}
	var pkg nodeInitManifest
	// Never print input JSON.
	if json.Unmarshal(data, &pkg) != nil {
		return app, fmt.Errorf("Invalid package.json in %s", directory)
	}
	// Development commands are not production entrypoints.
	if pkg.Scripts["start"] == "" {
		return app, fmt.Errorf("Node app in %s needs a start script or an authored Dockerfile", directory)
	}
	// Do not substitute dependency managers.
	if pkg.PackageManager != "" && !strings.HasPrefix(pkg.PackageManager, "npm@") {
		return app, fmt.Errorf("App uses another package manager; provide its Dockerfile")
	}
	// Prefer authored identity over a generic folder name.
	if pkg.Name != "" {
		app.Name = initName(pkg.Name)
		app.Project.Name = app.Name
	}
	// These frameworks document a 3000 listener and accept PORT.
	if pkg.Dependencies["next"] != "" || pkg.Dependencies["nuxt"] != "" {
		app.Project.Endpoint.Port = 3000
	}
	install := "npm install"
	// Existing npm locks remain authoritative.
	if initFileExists(filepath.Join(directory, "package-lock.json")) {
		install = "npm ci"
	} else {
		app.Notes = append(app.Notes, "Add a package-lock.json for reproducible dependency installation.")
	}
	app.Project.Env = map[string]string{"NODE_ENV": "production", "HOST": "0.0.0.0", "PORT": strconv.Itoa(app.Project.Endpoint.Port)}
	app.Files[filepath.Join(directory, "Dockerfile")] = []byte(fmt.Sprintf("FROM node:24.21.0-bookworm-slim\nWORKDIR /app\nCOPY package*.json ./\nRUN %s\nCOPY . .\nRUN npm run build --if-present && npm prune --omit=dev\nENV NODE_ENV=production HOST=0.0.0.0 PORT=%d\nUSER node\nEXPOSE %d\nCMD [\"npm\", \"start\"]\n", install, app.Project.Endpoint.Port, app.Project.Endpoint.Port))
	addInitIgnore(&app, "**/node_modules\n.git\n**/.env\n**/.env.*\n**/*.pem\n**/*.key\n**/.hostlier\n**/.hostlier.*\n**/.minion\n")
	app.Notes = append(app.Notes, "Review the generated Dockerfile and ensure the app listens on HOST and PORT.")
	return app, nil
}

// addInitIgnore preserves an existing ignore policy rather than rewriting the user's build inputs.
func addInitIgnore(app *initApp, content string) {
	path := filepath.Join(app.Directory, ".dockerignore")
	// Existing policies may contain deliberate allowlist exceptions and remain owned by the app.
	if initFileExists(path) {
		app.Notes = append(app.Notes, "Review existing .dockerignore to exclude credentials from the build context.")
		return
	}
	app.Files[path] = []byte(content)
}

// detectStaticInit exposes only public asset patterns, excluding arbitrary repository files.
func detectStaticInit(directory, architecture string) initApp {
	app := newInitApp(directory, "static", architecture)
	app.Files[filepath.Join(directory, "Dockerfile")] = []byte("FROM nginxinc/nginx-unprivileged:1.28-alpine\nCOPY . /usr/share/nginx/html/\nEXPOSE 8080\n")
	// A pre-existing policy cannot be assumed to protect a new static web root.
	if initFileExists(filepath.Join(directory, ".dockerignore")) {
		app.Notes = append(app.Notes, "Static site: verify .dockerignore allows only public assets before building.")
	}
	addInitIgnore(&app, "**\n!*.html\n!*.css\n!*.js\n!*.svg\n!*.png\n!*.ico\n!assets/\n!assets/**\n!images/\n!images/**\n!fonts/\n!fonts/**\n")
	return app
}

// detectMinionInit records build metadata without requiring consent or a completed deployment setup.
func detectMinionInit(directory, architecture string) (initApp, error) {
	app := newInitApp(directory, "minion", architecture)
	setup, err := readMinionInitSetup(directory)
	// Invalid authored metadata cannot become guessed settings.
	if err != nil {
		return app, err
	}
	app.Minion = setup
	// Preserve an authored target until an explicit init architecture override is applied.
	if value := setup.value("architecture"); value != "" {
		app.Project.Source.Container.Architecture = value
	}
	// Authored identities stay authoritative until an explicit override is supplied.
	if name := setup.value("name"); name != "" {
		app.Name = name
		app.Project.Name = name
	}
	context, err := minionInitContext(directory, defaultValue(setup.value("outdir"), "build"))
	// Inferred build output must remain inside this app.
	if err != nil {
		return app, err
	}
	app.Project.Source.Container.Context = context
	app.Project.Health = &model.Health{Path: "/healthz", TimeoutSeconds: 30}
	app.Project.Endpoint.Route = setup.value("route")
	app.Project.Fused = setup.Fused
	app.Project.Secrets = setup.Secrets
	return app, nil
}

// minionInitContext keeps inferred artifacts inside the app even before its build output exists.
func minionInitContext(directory, outdir string) (string, error) {
	clean := filepath.Clean(outdir)
	// External build contexts require an explicit --context selection rather than inferred config metadata.
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Minion outdir must remain inside the app directory; select external artifacts with --context")
	}
	context := filepath.Join(directory, clean, "artifact")
	// Existing ancestors must not redirect future build outputs outside the app.
	if err := inspectMinionContextAncestors(directory, context); err != nil {
		return "", err
	}
	return context, nil
}

// inspectMinionContextAncestors validates the nearest existing parent without requiring a completed Minion build.
func inspectMinionContextAncestors(directory, candidate string) error {
	_, err := os.Lstat(candidate)
	// A missing artifact can still be valid, but its eventual existing parent must stay inside the app.
	if os.IsNotExist(err) {
		return inspectMinionContextAncestors(directory, filepath.Dir(candidate))
	}
	// Access and filesystem errors must not be mistaken for a safe absent build directory.
	if err != nil {
		return err
	}
	actual, err := filepath.EvalSymlinks(candidate)
	// Broken or circular links are not portable artifact sources.
	if err != nil {
		return fmt.Errorf("Cannot resolve Minion artifact context")
	}
	relative, err := filepath.Rel(directory, actual)
	// Matching path prefixes are insufficient; compare path components after resolving all links.
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Minion outdir resolves outside the app directory; select external artifacts with --context")
	}
	return nil
}
