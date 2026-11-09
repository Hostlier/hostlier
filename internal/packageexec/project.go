package packageexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"hostlier/pkg/packagekit"
	"hostlier/pkg/workflow"
)

var originEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

// ProjectHooks keeps one executable snapshot for discovery, planning and packaging.
type ProjectHooks struct {
	config           map[string]any
	providedBindings []string
	name, root, path string
	cap              workflow.Capability
	cleanup          func()
}

// OpenProject pins both the project root and the installed package for one CLI invocation.
func (r Runner) OpenProject(ctx context.Context, name, root string) (*ProjectHooks, error) {
	root, err := filepath.Abs(root)
	// The child receives an absolute path independent of its working directory.
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	// Use the physical root for subsequent artifact containment checks.
	if err != nil {
		return nil, err
	}
	path, cleanup, err := r.localSnapshot(name)
	// Untrusted package directories fail before capability discovery.
	if err != nil {
		return nil, err
	}
	cap, err := r.describeSnapshot(ctx, path, name)
	// A failed negotiation transfers no snapshot ownership to the caller.
	if err != nil {
		cleanup()
		return nil, err
	}
	// Only one portable environment variable may receive the public origin.
	if cap.ProjectOriginEnv != "" && !originEnvName.MatchString(cap.ProjectOriginEnv) {
		cleanup()
		return nil, errors.New("invalid project origin environment name")
	}
	return &ProjectHooks{name: name, root: root, path: path, cap: cap, cleanup: cleanup}, nil
}

// OriginEnv names the app environment value that tracks a reviewed public origin.
func (h *ProjectHooks) OriginEnv() string {
	// A plain authored workload can have contributors without a primary builder.
	if h == nil {
		return ""
	}
	return h.cap.ProjectOriginEnv
}

// Close discards the executable snapshot and the temporary package home.
func (h *ProjectHooks) Close() { h.cleanup() }

// Has reports the capabilities advertised by the snapshot that will execute.
func (h *ProjectHooks) Has(hook string) bool { return h != nil && slices.Contains(h.cap.Hooks, hook) }

// Plan reads the package proposal without supplying a build destination.
func (h *ProjectHooks) Plan(ctx context.Context, arch string) (packagekit.ProjectPlanResponse, error) {
	req := packagekit.ProjectPlanRequest{Config: h.config, ProvidedBindings: h.providedBindings, Version: 1, Architecture: arch, ProjectDir: h.root}
	var out packagekit.ProjectPlanResponse
	// Stop before interpreting partial output from a failed subprocess.
	if err := h.invoke(ctx, "project-plan", req, &out, time.Minute); err != nil {
		return out, err
	}
	// A valid process exit may still carry a public configuration error.
	if err := h.response("project-plan", out.Version, out.Error); err != nil {
		return out, err
	}
	// A project must be an object before the CLI decodes its deployment fields.
	if len(out.Project) == 0 || out.Project[0] != '{' {
		return out, errors.New("project-plan did not return a project object")
	}
	return out, nil
}

// Package returns a verified relative path for the ordinary Hostlier artifact builder.
func (h *ProjectHooks) Package(ctx context.Context, outputDir, arch string, project, declarations json.RawMessage) (packagekit.ProjectPackageResponse, error) {
	var out packagekit.ProjectPackageResponse
	root, before, err := packageOutput(outputDir)
	// Keep previous or user-authored output out of this build.
	if err != nil {
		return out, err
	}
	req := packagekit.ProjectPackageRequest{Config: h.config, ProvidedBindings: h.providedBindings, Version: 1, ProjectDir: h.root, OutputDir: root, Architecture: arch, Project: project, Declarations: declarations}
	// A partial build is never passed to the Docker builder.
	if err = h.invoke(ctx, "project-package", req, &out, 15*time.Minute); err != nil {
		return out, err
	}
	// Check the protocol result before inspecting any child-selected path.
	if err = h.response("project-package", out.Version, out.Error); err != nil {
		return out, err
	}
	out.Artifact.Path, err = verifyProjectArtifact(root, before, out.Artifact)
	return out, err
}

// packageOutput records the owned directory identity before any external process runs.
func packageOutput(outputDir string) (string, os.FileInfo, error) {
	root, err := filepath.Abs(outputDir)
	// Output paths in the protocol are always absolute.
	if err != nil {
		return "", nil, err
	}
	before, err := os.Lstat(root)
	// Lstat prevents a symlink from serving as the output directory.
	if err != nil || !before.IsDir() {
		return "", nil, errors.New("packaging requires a dedicated output directory")
	}
	root, err = filepath.EvalSymlinks(root)
	// Parent aliases such as macOS /var are legitimate and must resolve consistently.
	if err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(root)
	// An empty destination keeps each build independent of earlier artifacts.
	if err != nil || len(entries) != 0 {
		return "", nil, errors.New("packaging requires an empty output directory")
	}
	return root, before, nil
}

// verifyProjectArtifact resolves symlinks against the original directory before accepting the context.
func verifyProjectArtifact(root string, before os.FileInfo, descriptor packagekit.ProjectArtifact) (string, error) {
	after, err := os.Lstat(root)
	// A package cannot swap the verified directory before returning its receipt.
	if err != nil || !os.SameFile(before, after) {
		return "", errors.New("package replaced its output directory")
	}
	// Only relative container contexts are understood by this version of the CLI.
	if descriptor.Kind != "container-context" || !filepath.IsLocal(descriptor.Path) {
		return "", errors.New("expected a container-context relative to the output directory")
	}
	artifact, err := filepath.EvalSymlinks(filepath.Join(root, descriptor.Path))
	// Broken links and missing outputs invalidate a successful process exit.
	if err != nil {
		return "", errors.New("package artifact does not exist")
	}
	rel, err := filepath.Rel(root, artifact)
	// Lexical containment alone cannot detect a symlink to the source tree.
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("package artifact escapes its output directory")
	}
	info, err := os.Stat(artifact)
	// Docker needs a directory even when the returned name has no extension.
	if err != nil || !info.IsDir() {
		return "", errors.New("package artifact must be a directory")
	}
	return rel, nil
}

// response distinguishes package diagnostics from transport failures.
func (h *ProjectHooks) response(hook string, version int, diagnostic string) error {
	// Reject new wire formats before interpreting their payloads.
	if version != 1 {
		return fmt.Errorf("invalid %s response version from package %q", hook, h.name)
	}
	// This field is the package-authorized public error, unlike stderr.
	if diagnostic != "" {
		return fmt.Errorf("package %q: %s", h.name, diagnostic)
	}
	return nil
}

// invoke sends a single bounded request to a negotiated local entry point.
func (h *ProjectHooks) invoke(ctx context.Context, hook string, req, out any, timeout time.Duration) error {
	// Missing hooks are reported without executing an unknown argument.
	if !h.Has(hook) {
		if hook == "init" { // Preserve the existing init diagnostic.
			return fmt.Errorf("package %q does not provide an init hook", h.name)
		}
		return fmt.Errorf("package %q does not provide a %s hook", h.name, hook)
	}
	data, err := json.Marshal(req)
	// Bound request memory independently of any particular package schema.
	if err != nil || len(data) > packagekit.MaxProtocolBytes {
		return errors.New("invalid or oversized project hook request")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.path, hook)
	// A native hook may launch npm, a compiler or another packager. Cancel the entire build on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Use the child process group created above, never the CLI process group.
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		// The process group can already have exited when cancellation arrives.
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Dir = h.root
	cmd.Env = h.environment(hook)
	cmd.Stdin = bytes.NewReader(data)
	cmd.WaitDelay = time.Second
	output := &limitedOutput{limit: packagekit.MaxProtocolBytes}
	cmd.Stdout = output
	// Never expose package stderr or accept a truncated protocol reply.
	if err = cmd.Run(); err != nil || output.overflow {
		return fmt.Errorf("package %q %s failed", h.name, hook)
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	// One response document is the complete result of the hook.
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid %s response from package %q", hook, h.name)
	}
	return nil
}

// environment separates local tool discovery from opt-in build credentials.
func (h *ProjectHooks) environment(hook string) []string {
	env := []string{"LANG=C", "HOME=" + filepath.Dir(h.path)}
	// The init contract uses only the basic system tools shipped on the host.
	if hook == "init" {
		return append(env, "PATH=/usr/local/bin:/usr/bin:/bin")
	}
	// Build tools must be discoverable on the user's machine. Never search a relative PATH entry.
	var paths []string
	// Preserve the operator’s tool lookup order.
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		// Relative entries would search unreviewed project directories.
		if filepath.IsAbs(p) {
			paths = append(paths, p)
		}
	}
	env = append(env, "PATH="+strings.Join(paths, string(os.PathListSeparator)))
	// Planning needs no access to build-time credentials.
	if hook != "project-package" {
		return env
	}
	// A fresh project may use the installed CLI to bootstrap its pinned compiler.
	if cli, err := os.Executable(); err == nil {
		// Provide a stable release location even when the PATH entry is a symlink.
		if resolved, err := filepath.EvalSymlinks(cli); err == nil {
			env = append(env, "HOSTLIER_CLI="+resolved)
		}
	}
	return append(env, h.declaredEnvironment()...)
}

// declaredEnvironment intersects a package's capability with the operator's allowed names.
func (h *ProjectHooks) declaredEnvironment() []string {
	var env []string
	// Installed third-party tools receive additional environment only by explicit operator selection.
	allow := strings.Split(os.Getenv("HOSTLIER_PACKAGE_ENV"), ",")
	// Inspect only environment names the package declared.
	for _, key := range h.cap.ProjectPackageEnv {
		// Independent packages need a matching operator opt-in.
		if !slices.Contains(allow, key) {
			continue
		}
		// A declared value cannot replace the runtime isolation settings.
		if key == "" || strings.ContainsAny(key, "=\x00") || slices.Contains([]string{"HOME", "LANG", "PATH", "HOSTLIER_CLI"}, key) {
			continue
		}
		value, set := projectEnvValue(key)
		// Leave unavailable credentials absent for packages with anonymous modes.
		if set {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// projectEnvValue preserves the standard config directory for packages using saved credentials.
func projectEnvValue(key string) (string, bool) {
	value, set := os.LookupEnv(key)
	// An unset XDG config location follows the per-user default.
	if key == "XDG_CONFIG_HOME" && !set {
		// Failure to resolve the user home leaves discovery disabled.
		if home, err := os.UserHomeDir(); err == nil {
			value, set = filepath.Join(home, ".config"), true
		}
	}
	return value, set
}

// SetProvidedBindings exposes public output names from the same reviewed contributions to the primary builder.
func (h *ProjectHooks) SetProvidedBindings(names []string) { h.providedBindings = names }

// Contribute executes a pinned package locally during planning, before any build or controller call.
func (h *ProjectHooks) Contribute(ctx context.Context, arch string) (packagekit.ProjectContribution, error) {
	var out packagekit.ProjectContribution
	req := packagekit.ProjectPlanRequest{Config: h.config, Version: 1, ProjectDir: h.root, Architecture: arch}
	// Reject an incomplete process response before treating it as a contribution.
	if err := h.invoke(ctx, "project-contribute", req, &out, time.Minute); err != nil {
		return out, err
	}
	return out, h.response("project-contribute", out.Version, out.Error)
}

// Capability returns the contract of this exact executable snapshot.
func (h *ProjectHooks) Capability() workflow.Capability { return h.cap }

// SetConfig pins this package's validated public inputs across all local hooks.
func (h *ProjectHooks) SetConfig(values map[string]any) { h.config = values }
