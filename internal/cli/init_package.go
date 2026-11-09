package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/pkg/packagekit"
)

// localPackageRunner resolves only the selected local package installation.
func (s *session) localPackageRunner(name string) (packageexec.Runner, error) {
	// Validate the name before resolving the executable path.
	if !packageexec.ValidName(name) {
		return packageexec.Runner{}, fmt.Errorf("Invalid package name")
	}
	runner := s.localInstallation()
	_, err := os.Lstat(filepath.Join(runner.Directory, name))
	// An adjacent binary is never an installed package.
	if os.IsNotExist(err) {
		return runner, fmt.Errorf("Package %q is not installed locally; use hostlier package install", name)
	}
	return runner, err
}

// localInstallation is shared by install, inventory, describe and hook execution.
func (s *session) localInstallation() packageexec.Runner {
	return packageexec.Runner{Directory: filepath.Join(defaultValue(s.flags["state-dir"], "/var/lib/hostlier"), "packages")}
}

// initPackage uses a single executable snapshot throughout project initialization.
func (s *session) initPackage(root, name string) error {
	runner, err := s.localPackageRunner(name)
	// Initialization requires a locally installed package.
	if err != nil {
		return err
	}
	req, err := s.packageInitRequest(root)
	// Check the operator's setup options before invoking the package.
	if err != nil {
		return err
	}
	hooks, err := runner.OpenProject(s.ctx, name, root)
	// Reuse the same executable for initialization and its scaffold.
	if err != nil {
		return err
	}
	defer hooks.Close()
	// A package without an initializer cannot interpret any init inputs.
	if !hooks.Has("init") {
		return fmt.Errorf("package %q does not provide an init hook", name)
	}
	contract, err := compileContract(hooks.Capability())
	// Never publish a manifest from an invalid package contract.
	if err != nil {
		return err
	}
	starter, err := initStarterSnapshot(root, contract, &req)
	// Capture enrolled inputs before the hook chooses files and workload settings.
	if err != nil {
		return err
	}
	result, err := hooks.Init(s.ctx, req)
	// A rejected initializer must not create any files.
	if err != nil {
		return err
	}
	app, document, err := initPackageManifest(root, result, name, req.Architecture == "" && hooks.Has("project-plan"))
	// Stage package files only after validating its project.
	if err != nil {
		return err
	}
	return s.finishPackageInit(root, app, document, result.Files, contract, starter)
}

// packageInitRequest validates optional CLI settings before running an initializer.
func (s *session) packageInitRequest(root string) (packagekit.InitRequest, error) {
	req := packagekit.InitRequest{Version: 1, UI: s.flags["ui"] == "true", Name: s.flags["name"], DefaultName: initName(filepath.Base(root)), Route: s.flags["route"], Architecture: s.flags["architecture"]}
	// An absent port lets the package choose its default.
	if port := s.flags["port"]; port != "" {
		var err error
		req.Port, err = strconv.Atoi(port)
		// Only valid listening ports are forwarded to init.
		if err != nil || req.Port < 1 || req.Port > 65535 {
			return req, fmt.Errorf("Invalid port")
		}
	}
	return req, nil
}

// initPackageManifest validates the proposal and adds only the selected package.
func initPackageManifest(root string, result packagekit.InitResponse, name string, portable bool) (string, string, error) {
	var project model.Project
	decoder := json.NewDecoder(bytes.NewReader(result.Project))
	decoder.DisallowUnknownFields()
	// Require the same deployable source shape as the planner.
	if decoder.Decode(&project) != nil || project.Version != 2 || project.Source == nil {
		return "", "", fmt.Errorf("Expected a Hostlier v2 project with a source from package %q", name)
	}
	check := project
	// Pending Fused identity is validated after consent.
	if minionInitPending(check) {
		check.Fused = nil
	}
	// Reject invalid deployment declarations before writing package files.
	if err := config.ValidateInput(check); err != nil {
		return "", "", err
	}
	// A planner can supply the selected target later. An implicit initializer default must not pin the file.
	if portable && project.Source.Container != nil {
		project.Source.Container.Architecture = ""
	}
	document, err := initProjectYAML(project, root)
	// Use the ordinary serializer for project fields and secret references.
	if err != nil {
		return "", "", err
	}
	return project.Name, document, nil
}

// finishPackageInit publishes the proposed files with the selected package's input scaffold.
func (s *session) finishPackageInit(root, app, document string, extras map[string]string, contract *packageContract, starter []byte) error {
	name := contract.Name
	lock, err := lockProjectDirectory(root)
	// Preserve additions by other CLI processes while composing the starter and workload.
	if err != nil {
		return err
	}
	defer lock.Close()
	path := filepath.Join(root, ".hostlier")
	manifest, originals, err := packageInitDocument(path, document, contract, starter)
	// A configured workload is never replaced by an initializer.
	if err != nil {
		return err
	}
	files := map[string]string{path: manifest}
	// Check every package-proposed file before writing any of them.
	for relative, data := range extras {
		// The host owns the manifest and the project directory boundary.
		if !filepath.IsLocal(relative) || filepath.Clean(relative) != relative || relative == "." || relative == ".hostlier" {
			return fmt.Errorf("Invalid scaffold path from package %q", name)
		}
		files[filepath.Join(root, relative)] = data
	}
	// A preview returns the complete proposal without creating files.
	if s.flags["dry-run"] != "" {
		return s.print(map[string]any{"app": app, "package": name, "files": files, "dryRun": true})
	}
	// Only report success once the complete scaffold is installed.
	if err := writeInitSetup(root, files, originals); err != nil {
		return err
	}
	return s.print(map[string]any{"app": app, "package": name, "created": minionInitCreated(files, originals), "updated": sortedInitKeys(originals)})
}

// packageInitDocument preserves the starter's enrollments and comments while adding the initialized workload.
func packageInitDocument(path, workload string, c *packageContract, raw []byte) (string, map[string]string, error) {
	// A direct init --package invocation can start without an existing manifest.
	if raw == nil {
		data, err := addPackageYAML([]byte(workload), c.Name, c)
		return string(data), nil, err
	}
	var starter, generated yaml.Node
	// Generated workload YAML comes from the typed serializer.
	if yaml.Unmarshal(raw, &starter) != nil || yaml.Unmarshal([]byte(workload), &generated) != nil {
		return "", nil, fmt.Errorf("package init requires a version 2 starter .hostlier")
	}
	root := starter.Content[0]
	proposal := generated.Content[0]
	// The starter owns version and packages, while the hook supplies the rest of the workload.
	for i := 0; i < len(proposal.Content); i += 2 {
		// The generator may include an empty package namespace.
		if key := proposal.Content[i].Value; key != "version" && key != "packages" {
			root.Content = append(root.Content, proposal.Content[i:i+2]...)
		}
	}
	// Fill missing inputs without replacing authored values.
	if _, err := scaffoldPackage(root, c.Name, c); err != nil {
		return "", nil, err
	}
	data, err := encodePackageYAML(&starter)
	return string(data), map[string]string{path: string(raw)}, err
}

// initStarterSnapshot supplies the hook only its enrolled public settings and retains the original bytes for atomic publication.
func initStarterSnapshot(root string, c *packageContract, req *packagekit.InitRequest) ([]byte, error) {
	path := filepath.Join(root, ".hostlier")
	raw, err := readInitFile(path)
	// A direct init has no prior settings.
	if os.IsNotExist(err) {
		return nil, nil
	}
	// Only regular project files may be updated.
	if err != nil {
		return nil, err
	}
	var starter struct {
		Version  int                       `yaml:"version"`
		Packages map[string]map[string]any `yaml:"packages"`
	}
	// A starter contains only enrollment settings. Refuse to overwrite an authored workload.
	if err := config.DecodeYAML(raw, &starter); err != nil || starter.Version != 2 {
		return nil, fmt.Errorf("package init requires a version 2 starter .hostlier containing only version and packages")
	}
	req.Config, err = initConfig(c, starter.Packages[c.Name])
	return raw, err
}

// initConfig gives a configured initializer its own inputs, leaving fresh scaffolds unconfigured.
func initConfig(c *packageContract, settings map[string]any) (map[string]any, error) {
	// Legacy init request shapes are preserved for packages without an input schema.
	if len(c.ConfigSchema) == 0 {
		return nil, nil
	}
	values := maps.Clone(settings)
	// Filter only placeholders. Explicit false, zero and empty strings are operator choices.
	maps.DeleteFunc(values, func(_ string, value any) bool { return value == nil })
	// A fresh initializer may propose its defaults before inputs are chosen.
	if len(values) == 0 {
		return nil, nil
	}
	return c.inputs(values)
}
