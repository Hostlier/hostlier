package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/pkg/packagekit"
)

// runHook keeps protocol failures distinct from public configuration errors.
func runHook[I, O any](fn func(I) (O, error)) {
	limited := io.LimitReader(os.Stdin, packagekit.MaxProtocolBytes+1)
	data, err := io.ReadAll(limited)
	var req I
	// Distinguish malformed protocol requests from errors in a valid project.
	if err != nil || len(data) > packagekit.MaxProtocolBytes || json.Unmarshal(data, &req) != nil {
		os.Exit(2)
	}
	result, err := fn(req)
	enc := json.NewEncoder(os.Stdout)
	// Report user-correctable failures in a versioned response.
	if err != nil {
		// A transport error prevents Hostlier from reading the public diagnostic.
		if enc.Encode(struct {
			Version int    `json:"version"`
			Error   string `json:"error"`
		}{1, err.Error()}) != nil {
			os.Exit(1)
		}
		return
	}
	// Treat a partial success response as a failed hook invocation.
	if enc.Encode(result) != nil {
		os.Exit(1)
	}
}

// projectFile retains the original JSON alongside the fields needed to plan.
type projectFile struct {
	settings
	raw map[string]json.RawMessage
}

// readApp keeps opaque extensions so older compilers can consume the authored settings.
func readApp(root string, inputs map[string]any) (projectFile, error) {
	app := projectFile{raw: map[string]json.RawMessage{}}
	path := filepath.Join(root, "minion.json")
	info, err := os.Lstat(path)
	// New projects can declare their build inputs entirely in .hostlier.
	if os.IsNotExist(err) {
		return standaloneApp(root, inputs)
	}
	// Reject symlinks, special files and oversized app definitions before reading.
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return app, errors.New("Expected a regular minion.json under 1 MiB")
	}
	b, err := os.ReadFile(path)
	// Decode both representations from the same bytes to keep their settings consistent.
	if err != nil || len(b) > 1<<20 || json.Unmarshal(b, &app.settings) != nil || json.Unmarshal(b, &app.raw) != nil || app.raw == nil {
		return app, errors.New("Invalid or unnamed minion.json")
	}
	return mergeBuildInputs(root, app, inputs)
}

// validTarget requires a supported protocol and an explicit platform target.
func validTarget(version int, root, arch string) error {
	// Explicit platform and project inputs keep hooks independent of the working directory.
	if version != 1 || !filepath.IsAbs(root) || (arch != "arm64" && arch != "amd64") {
		return errors.New("Invalid Minion project hook target")
	}
	return nil
}

// plan derives the review from static project declarations and never installs a framework.
func plan(req packagekit.ProjectPlanRequest) (packagekit.ProjectPlanResponse, error) {
	var out packagekit.ProjectPlanResponse
	// Validate the request before reading project files.
	if err := validTarget(req.Version, req.ProjectDir, req.Architecture); err != nil {
		return out, err
	}
	app, err := readApp(req.ProjectDir, req.Config)
	// Planning requires a named, decodable app definition.
	if err != nil {
		return out, err
	}
	p, err := effectiveProject(req.ProjectDir, app, req.Architecture)
	// An inconsistent deploy file must be corrected before showing a plan.
	if err != nil {
		return out, err
	}
	out.Project, err = json.Marshal(p)
	// Return a reviewable project only if the protocol can represent it.
	if err != nil {
		return out, err
	}
	out.Declarations, err = consentScope(req.ProjectDir, app)
	out.Version = 1
	return out, err
}

// effectiveProject preserves the author's full deployment topology. Only the primary app context is produced by this hook.
func effectiveProject(root string, app projectFile, arch string) (model.Project, error) {
	p := model.Project{Version: 2, Name: app.Name, Source: &model.Source{Container: &model.ContainerSource{}}, Endpoint: &model.Endpoint{Port: 8080, Route: app.Route, Exposure: "public"}, Health: &model.Health{Path: "/healthz", TimeoutSeconds: 30}, Env: map[string]string{}, Fused: app.Fused, Secrets: app.Secrets}
	path := filepath.Join(root, ".hostlier")
	// Use an authored deployment when present, including its endpoints and dependencies.
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		input, err := config.ReadProjectSources(path, "")
		// Never substitute scaffold defaults for an unreadable deployment file.
		if err != nil {
			return p, err
		}
		p = input.Project
		fillProjectDefaults(&p, app)
		// The primary artifact must still belong to the Minion named in the deployment.
		if err := validateSource(p, app); err != nil {
			return p, err
		}
		p.Fused = app.Fused
	}
	p.Source.Container.Architecture = arch
	p.Source.Container.Context = "."
	// Derive the public origin from the endpoint that will serve the application.
	if err := setOrigin(&p); err != nil {
		return p, err
	}
	return p, nil
}

// fillProjectDefaults completes a v2 enrollment while preserving authored topology.
func fillProjectDefaults(p *model.Project, app projectFile) {
	p.Name = cmp.Or(p.Name, app.Name)
	// An absent source is normal for a fresh package add.
	if p.Source == nil && p.Image == "" {
		p.Source = &model.Source{Container: &model.ContainerSource{Context: filepath.Join(cmp.Or(app.Outdir, "build"), "artifact")}}
		// Preserve explicit health and secret declarations.
		if p.Health == nil {
			p.Health = &model.Health{Path: "/healthz", TimeoutSeconds: 30}
		}
		// Inherit declared secret references when the fresh deployment has none.
		if p.Secrets == nil {
			p.Secrets = app.Secrets
		}
	}
	// A user-selected endpoint takes precedence over the framework's default listener.
	if p.Endpoint == nil && len(p.Endpoints) == 0 {
		p.Endpoint = &model.Endpoint{Port: 8080, Route: app.Route, Exposure: "public"}
	}
}

// validateSource binds the plan to the named local app without dropping its authored settings.
func validateSource(p model.Project, app projectFile) error {
	// Older deployments omit package, but an explicit selection must agree.
	if p.Package != "" && p.Package != "minion" {
		return errors.New("Deployment file selects another package")
	}
	// The packager only builds this application's local container context.
	if p.Name != app.Name || p.Source == nil || p.Source.Container == nil {
		return errors.New("Minion requires its own named local container source")
	}
	outdir := cmp.Or(app.Outdir, "build")
	// An authored context must point at this application's build output.
	if !filepath.IsLocal(outdir) || filepath.Clean(p.Source.Container.Context) != filepath.Join(outdir, "artifact") {
		return errors.New("Minion build context differs from the deployment file")
	}
	// Conflicting bindings could send a token to an unintended Engine.
	if p.Fused != nil && !reflect.DeepEqual(p.Fused, app.Fused) {
		return errors.New("Minion Fused binding differs from the deployment file")
	}
	return nil
}

// primary returns the effective HTTP endpoint of a single- or multi-endpoint project.
func primary(p model.Project) *model.Endpoint {
	// Named endpoints take precedence over the legacy singleton field.
	if e, ok := p.Endpoints[model.PrimaryEndpointName(p)]; ok {
		return &e
	}
	return p.Endpoint
}

// setOrigin restricts Minion to same-origin HTTP actions and a root HTTPS route.
func setOrigin(p *model.Project) error {
	e := primary(*p)
	// A runtime port must come from an actual deployment endpoint.
	if e == nil {
		return errors.New("Minion requires a configured HTTP endpoint")
	}
	// Projects without custom variables still need Minion's runtime settings.
	if p.Env == nil {
		p.Env = map[string]string{}
	}
	p.Env["PORT"] = strconv.Itoa(e.Port)
	// Hostlier may prompt for the hostname after planning.
	if e.Route == "" {
		return nil
	}
	route, err := config.ParseRoute(e.Route, nil, "")
	// Minion serves actions from the origin root, so path routes are unsupported.
	if err != nil || route.Path != "/" {
		return errors.New("Minion requires a root HTTPS hostname")
	}
	p.Env["MINION_ORIGIN"] = "https://" + route.Hostname
	return nil
}

// jsonEqual compares wire objects independent of formatting and key order.
func jsonEqual(a, b []byte) bool {
	// An absent declaration differs from an explicitly declared JSON value.
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

// A CLI-selected deployment hostname may differ from the app default. Everything else must still match.
func validateReviewed(current, reviewed model.Project) error {
	// A removed primary endpoint invalidates the review.
	if primary(reviewed) == nil || primary(current) == nil {
		return errors.New("Reviewed project has no HTTP endpoint")
	}
	route := primary(reviewed).Route
	// Reflect the CLI's hostname in the legacy endpoint representation.
	if current.Endpoint != nil {
		current.Endpoint.Route = route
	}
	// Update only the named primary endpoint, leaving sidecar routes intact.
	if name := model.PrimaryEndpointName(current); name != "" {
		e := current.Endpoints[name]
		e.Route = route
		current.Endpoints[name] = e
	}
	// Route is a legacy compatibility field supplied by the generic CLI when it resolves an override.
	current.Route = reviewed.Route
	// Check the overridden hostname against Minion's origin rules as well.
	if err := setOrigin(&current); err != nil {
		return err
	}
	a, _ := json.Marshal(current)
	b, _ := json.Marshal(reviewed)
	// Any remaining difference requires a new project review.
	if !jsonEqual(a, b) {
		return errors.New("Project declarations changed. Run plan again.")
	}
	return nil
}

// readScope accepts only a bounded regular manifest from the compiler.
func readScope(path string) (json.RawMessage, error) {
	info, err := os.Lstat(path)
	// Never block on a special file or follow a compiler-emitted symlink.
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, errors.New("Expected a regular compiled Fused access manifest under 64 KiB")
	}
	f, err := os.Open(path)
	// A missing manifest cannot authorize access.
	if err != nil {
		return nil, errors.New("Compiler did not emit the reviewed Fused access manifest")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	// Enforce the limit on the bytes read as well as the initial file size.
	if err != nil || len(raw) > 65536 || !json.Valid(raw) {
		return nil, fmt.Errorf("Invalid compiled Fused access manifest")
	}
	return bytes.TrimSpace(raw), nil
}
