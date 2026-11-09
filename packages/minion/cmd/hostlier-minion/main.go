// Command hostlier-minion provides Minion's local Hostlier package hooks.
package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/packages/minion/scaffold"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/workflow"
)

// main exposes one versioned package operation per process.
func main() {
	// Hook payloads arrive on stdin. Only the operation belongs on the command line.
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	// Dispatch the stages independently so planning cannot invoke the packager by accident.
	switch os.Args[1] {
	// Advertise the environment and operations this binary understands.
	case "describe":
		// A truncated capability document cannot be used to discover hooks.
		if json.NewEncoder(os.Stdout).Encode(workflow.Capability{Version: 1, Name: "minion", Hooks: []string{"init", "project-plan", "project-package"}, ProjectOriginEnv: "MINION_ORIGIN", ProjectPackageEnv: []string{"FUSED_API_KEY", "XDG_CONFIG_HOME", "HOSTLIER_MINION_PACKAGE"}, ConfigSchema: buildInputSchema(), OutputSchema: map[string]any{}}) != nil {
			os.Exit(1)
		}
	// Let init propose files for Hostlier to write after review.
	case "init":
		runHook(initProject)
	// Return the topology and permission declarations before running installation.
	case "project-plan":
		runHook(plan)
	// Build into the output directory allocated by the CLI.
	case "project-package":
		runHook(build)
	// Reject unknown hooks so newer callers can detect a version mismatch.
	default:
		os.Exit(2)
	}
}

// initProject adapts the optional init hook to the common versioned transport.
func initProject(req packagekit.InitRequest) (packagekit.InitResponse, error) {
	// Validate the protocol before proposing files.
	if req.Version != 1 {
		return packagekit.InitResponse{}, errors.New("Invalid Minion init protocol")
	}
	out, err := prepare(req)
	out.Version = 1
	return out, err
}

// settings contains the deployment fields shared by init and plan.
type settings struct {
	Name, Entry, Outdir, Architecture, Route string
	Fused                                    *model.FusedConnection
	Secrets                                  map[string]string
}

// prepare proposes missing starter files and a deployment for either a new or an existing Minion.
func prepare(req packagekit.InitRequest) (packagekit.InitResponse, error) {
	result := packagekit.InitResponse{Files: map[string]string{}}
	enrolled, _ := req.Config["name"].(string)
	// A command-line override must agree with the package configuration retained in the manifest.
	if req.Name != "" && enrolled != "" && req.Name != enrolled {
		return result, errors.New("--name must match packages.minion.name")
	}
	req.Name = cmp.Or(req.Name, enrolled)
	app, err := initSettings(req, result.Files)
	// Propagate an invalid project rather than replacing it with a scaffold.
	if err != nil {
		return result, err
	}
	app.Outdir = cmp.Or(app.Outdir, "build")
	// Keep the declared build context inside the project.
	if !filepath.IsLocal(app.Outdir) {
		return result, errors.New("Minion outdir must stay within the app directory")
	}
	app.Architecture = cmp.Or(req.Architecture, app.Architecture, "amd64")
	req.Port = cmp.Or(req.Port, 8080)
	app.Route = cmp.Or(req.Route, app.Route)
	env := map[string]string{"PORT": strconv.Itoa(req.Port)}
	// Leave hostname selection to deploy when it was omitted at init.
	if app.Route != "" {
		route, err := config.ParseRoute(app.Route, nil, "")
		// The UI and action client share a root HTTPS origin.
		if err != nil || route.Path != "/" {
			return result, errors.New("Use a root HTTPS deployment hostname")
		}
		app.Route = route.Hostname
		env["MINION_ORIGIN"] = "https://" + route.Hostname
	}
	project := model.Project{Version: 2, Name: app.Name, Source: &model.Source{Container: &model.ContainerSource{Context: filepath.Join(app.Outdir, "artifact"), Architecture: app.Architecture}}, Endpoint: &model.Endpoint{Exposure: "public", Port: req.Port, Route: app.Route}, Health: &model.Health{Path: "/healthz", TimeoutSeconds: 30}, Env: env, Fused: app.Fused, Secrets: app.Secrets}
	result.Project, err = json.Marshal(project)
	// Return a project only when its entire configuration can be encoded.
	if err != nil {
		return result, fmt.Errorf("Cannot encode the Minion project")
	}
	return result, nil
}

// initSettings preserves existing apps and proposes only absent scaffold files for new ones.
func initSettings(req packagekit.InitRequest, files map[string]string) (settings, error) {
	info, err := os.Lstat("minion.json")
	// A missing project file identifies a fresh scaffold.
	if os.IsNotExist(err) {
		app := settings{Name: cmp.Or(req.Name, req.DefaultName)}
		entry, _ := req.Config["entry"].(string)
		return app, missingScaffold(app.Name, cmp.Or(entry, "app.ts"), req.UI, files)
	}
	// A template choice must not silently replace the configuration of an existing app.
	if req.UI && err == nil {
		return settings{}, errors.New("--ui creates a new Minion UI starter; this directory already contains minion.json")
	}
	// Reject special files and enforce the project size limit before opening it.
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return settings{}, errors.New("minion.json must be a regular file under 1 MiB")
	}
	return existingInitSettings(req.Name)
}

// existingInitSettings verifies the identity of an app before adopting it.
func existingInitSettings(name string) (settings, error) {
	var app settings
	data, err := os.ReadFile("minion.json")
	// Preserve existing settings only when they form a readable project document.
	if err != nil || json.Unmarshal(data, &app) != nil {
		return app, errors.New("Invalid minion.json")
	}
	// Require a stable identity for an existing project.
	if app.Name == "" {
		return app, errors.New("Set the app name in minion.json before initializing")
	}
	// An explicit CLI name cannot rename an existing deployment.
	if name != "" && name != app.Name {
		return app, errors.New("--name must match the existing minion.json name")
	}
	return app, nil
}

// missingScaffold proposes starter files without overwriting existing work.
func missingScaffold(name, entry string, ui bool, files map[string]string) error {
	// Propose only the starter files absent from this directory.
	if !filepath.IsLocal(entry) || filepath.Clean(entry) != entry || entry == "." {
		return errors.New("packages.minion.entry must be a project-relative file")
	}
	proposed := scaffold.Files(name, entry, ui)
	for _, f := range proposed[1:] { // The server entry cannot overwrite a config or UI scaffold file.
		if f.Path == entry {
			return errors.New("packages.minion.entry conflicts with a starter file")
		}
	}
	// Preserve all existing project files during initialization.
	for _, f := range proposed {
		_, err := os.Lstat(f.Path)
		// Existing files, including symlinks, stay outside the proposal.
		if os.IsNotExist(err) {
			files[f.Path] = f.Content
			continue
		}
		// Permission errors must not be mistaken for absent files.
		if err != nil {
			return errors.New("Cannot inspect starter files")
		}
	}
	return nil
}
