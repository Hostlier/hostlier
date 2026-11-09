package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hostlier/internal/model"
)

// minionCommand runs the project framework locally without requiring a controller profile.
func (s *session) minionCommand() error {
	if s.sub == "" { // Bare product commands expose their supported local workflow.
		s.message(s.leafHelpMustMinion())
		return nil
	}
	if err := s.validateMinionArguments(); err != nil { // Reject unrelated deployment authority before executing Node or npm.
		return err
	}
	if err := s.requireMinionNode(); err != nil { // Both the framework and package installation require the supported runtime.
		return err
	}
	if s.sub == "install" { // Installation can precede authoring minion.json.
		return s.installMinion(s.minionDirectory(), s.flags["file"])
	}
	config := s.minionConfig()
	if info, err := os.Stat(config); err != nil || !info.Mode().IsRegular() { // Missing projects must not cause an incidental dependency installation.
		return model.Err("MINION_CONFIG", "Cannot read Minion config: "+config)
	}
	directory := filepath.Dir(config)
	script, err := s.ensureMinionCLI(directory)
	// Do not start an incomplete or missing framework installation.
	if err != nil {
		return err
	}
	args := []string{script, s.sub, config}
	if port := s.flags["port"]; port != "" { // Only dev accepts the validated local listener port.
		args = append(args, "--port", port)
	}
	return s.runMinionProcess(directory, "node", args, s.in, s.out, s.errOut)
}

// ensureMinionCLI installs Hostlier's framework only when the project has no local CLI.
func (s *session) ensureMinionCLI(directory string) (string, error) {
	script, err := findMinionCLI(directory)
	// Existing project installations remain authoritative for local development.
	if err == nil {
		return script, nil
	}
	// A failed install cannot safely resume the requested command.
	if err := s.installMinion(directory, ""); err != nil {
		return "", err
	}
	return findMinionCLI(directory)
}

// leafHelpMustMinion keeps bare product help independent of profile and dependency discovery.
func (s *session) leafHelpMustMinion() string {
	help := commandHelp["minion"]
	return "Minion — build and test deterministic apps\n\nUsage:\n  hostlier " + strings.ReplaceAll(help.usage, "\n", "\n  hostlier ") + "\n\n" + help.note + "\n\nFull guide: hostlier skills show minion"
}

// validateMinionArguments admits only local framework actions and their narrowly scoped flags.
func (s *session) validateMinionArguments() error {
	allowed := map[string]bool{"file": true}
	switch s.sub { // Each action has a distinct argument contract rather than forwarding arbitrary CLI flags.
	case "dev": // Only development opens a local listener.
		allowed["port"] = true
	case "prepare", "build": // Preparation and packaging only accept a config.
	case "install": // Installation can select an empty project directory.
		allowed["directory"] = true
	default: // Unknown actions never reach the framework.
		return model.Err("ARGUMENTS", "Use hostlier init --package minion to create an app, or hostlier minion dev, prepare, build, or install")
	}
	// Config selectors cannot silently override one another.
	if err := s.validateMinionTarget(); err != nil {
		return err
	}
	// Refuse controller, credential and unrelated process options.
	for key, value := range s.flags {
		// Empty options are never forwarded to the framework.
		if !allowed[key] || value == "" {
			return model.Err("ARGUMENTS", "Unsupported Minion option --"+key)
		}
	}
	return validateMinionPort(s.flags["port"])
}

// validateMinionTarget ensures config selectors cannot override one another.
func (s *session) validateMinionTarget() error {
	// Install only selects a directory; other actions accept one config selector.
	if len(s.parsed.args) > 3 || (s.sub == "install" && s.target != "") || (s.target != "" && s.flags["file"] != "") {
		return model.Err("ARGUMENTS", "Select one Minion config with a positional path or --file")
	}
	return nil
}

// validateMinionPort confines the development listener to a valid TCP port.
func validateMinionPort(value string) error {
	// An omitted port uses the framework default.
	if value == "" {
		return nil
	}
	port, err := strconv.Atoi(value)
	// Reject invalid values before Node or npm can start.
	if err != nil || port < 0 || port > 65535 {
		return model.Err("ARGUMENTS", "Minion port must be between 0 and 65535")
	}
	return nil
}

// minionConfig resolves authored paths relative to the invocation rather than the framework package.
func (s *session) minionConfig() string {
	value := defaultValue(s.target, defaultValue(s.flags["file"], "minion.json"))
	// Absolute config paths keep their intended project root.
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(s.cwd, value)
}

// minionDirectory selects the local package installation directory without changing the shell's cwd.
func (s *session) minionDirectory() string {
	value := s.flags["directory"]
	// Default installs belong to the current project.
	if value == "" {
		return s.cwd
	}
	// Preserve explicit absolute project selection.
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(s.cwd, value)
}

// requireMinionNode checks the runtime before npm can modify the project lockfile.
func (s *session) requireMinionNode() error {
	var out bytes.Buffer
	if err := s.runMinionProcess(s.cwd, "node", []string{"--version"}, nil, &out, s.errOut); err != nil { // A missing interpreter needs an actionable local setup error.
		return model.Err("MINION_NODE", "Install Node.js 24 or newer to use Hostlier Minion")
	}
	major, err := strconv.Atoi(strings.Split(strings.TrimPrefix(strings.TrimSpace(out.String()), "v"), ".")[0])
	// Older runtimes cannot honor the framework's execution contract.
	if err != nil || major < 24 {
		return model.Err("MINION_NODE", "Hostlier Minion requires Node.js 24 or newer")
	}
	return nil
}

// findMinionCLI resolves a locally installed framework, including normal npm workspace hoisting.
func findMinionCLI(directory string) (string, error) {
	for current := directory; ; current = filepath.Dir(current) { // Resolve only filesystem packages; never request a package from npm implicitly.
		candidate := filepath.Join(current, "node_modules", "@fused", "minion", "dist", "src", "cli.js")
		// An installed executable takes precedence over source checkout tooling.
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
		// Stop at the filesystem root instead of looping forever.
		if filepath.Dir(current) == current {
			break
		}
	}
	return "", model.Err("MINION_MISSING", "Minion is not installed in this project; run hostlier minion install")
}

// installMinion installs the release's exact framework archive rather than a floating registry name.
func (s *session) installMinion(directory, explicit string) error {
	archive, cleanup, err := s.minionArchive(explicit)
	// Missing release payloads must not trigger an unrelated registry download.
	if err != nil {
		return err
	}
	defer cleanup()
	// Create only the explicitly selected local project directory.
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	archive, err = stageMinionArchive(directory, archive)
	// Keep the saved npm dependency valid after temporary source packaging is cleaned up.
	if err != nil {
		return err
	}
	return s.runMinionProcess(directory, "npm", []string{"install", "--ignore-scripts", "--no-audit", "--no-fund", "--save", archive}, s.in, s.out, s.errOut)
}

// stageMinionArchive preserves the package in the project so npm lockfiles never reference a temporary archive.
func stageMinionArchive(directory, archive string) (string, error) {
	destination := filepath.Join(directory, ".minion", "framework", filepath.Base(archive))
	// A previously staged package is already durable and must not be truncated by copying onto itself.
	if filepath.Clean(archive) == destination {
		return archive, nil
	}
	data, err := os.ReadFile(archive)
	// Preserve any existing dependency archive if the requested package cannot be read.
	if err != nil {
		return "", err
	}
	// The vendor directory belongs to this project and contains no runtime credentials.
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return "", err
	}
	// The package is durable before npm records it as a dependency.
	if err := os.WriteFile(destination, data, 0644); err != nil {
		return "", err
	}
	return destination, nil
}

// minionArchive locates a bundled release or materializes the local source package for development.
func (s *session) minionArchive(explicit string) (string, func(), error) {
	value := explicit
	// Source/test invocations may select an exact local archive without changing installed releases.
	if value == "" {
		value = s.minionEnvironment("HOSTLIER_MINION_PACKAGE")
	}
	// Explicit archive selection never accepts an npm name or URL.
	if value != "" {
		return s.explicitMinionArchive(value)
	}
	return s.bundledMinionArchive()
}

// explicitMinionArchive validates a local package override before any npm invocation.
func (s *session) explicitMinionArchive(value string) (string, func(), error) {
	// Relative overrides follow the command's working directory.
	if !filepath.IsAbs(value) {
		value = filepath.Join(s.cwd, value)
	}
	info, err := os.Stat(value)
	// Only a regular archive can become this project's framework dependency.
	if err != nil || !info.Mode().IsRegular() || !strings.HasSuffix(value, ".tgz") {
		return "", minionNoCleanup, model.Err("MINION_PACKAGE", "Supply an existing local Minion .tgz archive")
	}
	return value, minionNoCleanup, nil
}

// minionNoCleanup leaves durable release and explicitly selected archives intact.
func minionNoCleanup() {}

// bundledMinionArchive searches the installed binary's ancestors for its exact package.
func (s *session) bundledMinionArchive() (string, func(), error) {
	executable, err := os.Executable()
	// A release archive is resolved relative to its actual executable.
	if err != nil {
		return "", minionNoCleanup, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	// Symlinked installation paths must resolve before searching for release payloads.
	if err != nil {
		return "", minionNoCleanup, err
	}
	for directory := filepath.Dir(executable); ; directory = filepath.Dir(directory) { // Development builds may live under the repository's dist directory.
		matches, _ := filepath.Glob(filepath.Join(directory, "minion", "fused-minion-*.tgz"))
		// A release carries exactly one framework version.
		if len(matches) == 1 {
			return matches[0], minionNoCleanup, nil
		}
		// Never guess which local package should become executable code.
		if len(matches) > 1 {
			return "", minionNoCleanup, model.Err("MINION_PACKAGE", "Multiple bundled Minion versions; select one with HOSTLIER_MINION_PACKAGE")
		}
		source := filepath.Join(directory, "packages", "minion", "package.json")
		// Local source builds use the repository's canonical pack command.
		if _, err := os.Stat(source); err == nil {
			return s.packSourceMinion(directory)
		}
		// Do not search outside the executable's ancestor chain.
		if filepath.Dir(directory) == directory {
			break
		}
	}
	return "", minionNoCleanup, model.Err("MINION_PACKAGE", "The Hostlier release is missing its minion package; reinstall the complete CLI archive or set HOSTLIER_MINION_PACKAGE")
}

// packSourceMinion stages a source checkout package without putting generated archives in the repository.
func (s *session) packSourceMinion(root string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "hostlier-minion-package-")
	// Failed staging cannot proceed with a partial output path.
	if err != nil {
		return "", minionNoCleanup, err
	}
	// The staging directory belongs exclusively to this packaging attempt.
	cleanup := func() { _ = os.RemoveAll(directory) }
	args := []string{filepath.Join(root, "scripts", "package-minion.sh"), directory}
	// Keep npm diagnostics visible without claiming an installed framework.
	if err := s.runMinionProcess(root, "sh", args, nil, s.errOut, s.errOut); err != nil {
		cleanup()
		return "", minionNoCleanup, err
	}
	matches, _ := filepath.Glob(filepath.Join(directory, "fused-minion-*.tgz"))
	// The package installer must consume an exact build result.
	if len(matches) != 1 {
		cleanup()
		return "", minionNoCleanup, model.Err("MINION_PACKAGE", "Minion packaging did not produce one archive")
	}
	return matches[0], cleanup, nil
}

// minionEnvironment permits deterministic tests without reading unrelated host credentials.
func (s *session) minionEnvironment(name string) string {
	// Injected environments fully own local option lookup.
	if s.options.Environment != nil {
		return s.options.Environment[name]
	}
	return os.Getenv(name)
}

// runMinionProcess forwards streams and cancellation without a shell or controller connection.
func (s *session) runMinionProcess(directory, executable string, args []string, in io.Reader, out, errOut io.Writer) error {
	// Test doubles observe literal process arguments instead of executing npm.
	if s.options.RunMinion != nil {
		return s.options.RunMinion(s.ctx, directory, executable, args, in, out, errOut)
	}
	command := exec.CommandContext(s.ctx, executable, args...)
	command.Dir = directory
	command.Stdin = in
	command.Stdout = out
	command.Stderr = errOut
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) } // Let the dev runner close its listener and SDK channel before forced termination.
	command.WaitDelay = 15 * time.Second
	// Process errors retain exit status without printing environment credentials.
	if err := command.Run(); err != nil {
		// Gracefully drained dev processes may report context cancellation after exiting successfully.
		if errors.Is(err, context.Canceled) && command.ProcessState != nil && command.ProcessState.Success() {
			return nil
		}
		return fmt.Errorf("Minion %s failed: %w", executable, err)
	}
	return nil
}
