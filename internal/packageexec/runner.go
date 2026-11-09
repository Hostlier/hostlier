// Package packageexec executes locally installed, digest-pinned packages over bounded private pipes.
package packageexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"hostlier/pkg/packagekit"
	"hostlier/pkg/workflow"
)

// ErrMissingDeployHook contains no subprocess output and is safe for a planning error.
var ErrMissingDeployHook = errors.New("controller package does not provide the project-deploy hook")

var packageName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Runner loads only administrator-installed binaries under a protected package directory.
type Runner struct {
	Directory   string
	PlanTimeout time.Duration
}

// Plan runs read-only preparation within an administrator-selected bounded deadline.
func (r Runner) Plan(ctx context.Context, req workflow.Request) (workflow.Prepared, error) {
	req.Action = "plan"
	response, digest, err := r.invoke(ctx, req, "", r.planningTimeout())
	// Packages must return only the response variant matching the requested operation.
	if errors.Is(err, ErrMissingDeployHook) {
		return workflow.Prepared{}, err
	}
	// The controller can safely distinguish an unsupported hook from provider errors.
	if err != nil || response.Prepared == nil || response.Result != nil || response.Inspection != nil {
		return workflow.Prepared{}, errors.New("package planning failed")
	}
	p := *response.Prepared
	p.Digest = digest
	// Reviews must be bounded and include an execution payload.
	if len(p.Summary) == 0 || len(p.Summary) > 32 || !json.Valid(p.Payload) {
		return workflow.Prepared{}, errors.New("invalid package plan")
	}
	return p, nil
}

// Apply executes precisely the binary reviewed in the approved plan.
func (r Runner) Apply(ctx context.Context, req workflow.Request, digest string) (workflow.Result, error) {
	req.Action = "apply"
	response, _, err := r.invoke(ctx, req, digest, 15*time.Minute)
	// Ignore all private diagnostics and reject malformed package results.
	if err != nil || response.Result == nil || response.Prepared != nil || response.Inspection != nil {
		return workflow.Result{}, errors.New("package execution failed; reconcile before retry")
	}
	result := *response.Result
	// Result state and outputs are encrypted by the caller before use.
	if !workflow.ValidResult(result) {
		return workflow.Result{}, errors.New("invalid package result")
	}
	return result, nil
}

// invoke snapshots verified executable bytes to avoid changing binaries between approval and execution.
func (r Runner) invoke(ctx context.Context, req workflow.Request, expected string, timeout time.Duration) (workflow.Response, string, error) {
	path, digest, cleanup, err := r.snapshot(req.Step.Package, expected)
	// Missing, unsafe or changed installations cannot run.
	if err != nil {
		return workflow.Response{}, "", err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req.Version = 1
	data, err := json.Marshal(req)
	// Bound private request size before starting a child process.
	if err != nil || len(data) > packagekit.MaxProtocolBytes {
		return workflow.Response{}, "", errors.New("package request too large")
	}
	entry, err := r.workflowEntry(ctx, path, req)
	// Missing capability declarations prevent all hook dispatch.
	if err != nil {
		return workflow.Response{}, "", err
	}
	command := exec.CommandContext(ctx, path, entry)
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C", "HOME=" + r.Directory}
	command.Stdin = bytes.NewReader(data)
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	output := &limitedOutput{limit: packagekit.MaxProtocolBytes}
	command.Stdout = output
	// Process errors must never include stdout, stderr or request contents.
	if command.Run() != nil || output.overflow {
		return workflow.Response{}, "", errors.New("package process failed")
	}
	var response workflow.Response
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	// Accept exactly one versioned response document.
	if decoder.Decode(&response) != nil || decoder.Decode(new(any)) != io.EOF || response.Version != 1 {
		return response, "", errors.New("invalid package response")
	}
	return response, digest, nil
}

// workflowEntry negotiates the optional hook against the very bytes about to execute.
func (r Runner) workflowEntry(ctx context.Context, path string, req workflow.Request) (string, error) {
	// Existing resource executables continue to receive the serve entry point.
	if req.Step.Hook == "" {
		return "serve", nil
	}
	// Reject arbitrary argv selectors and hooks missing the approved project.
	if req.Step.Hook != workflow.ProjectDeployHook || req.Step.Phase != "beforeDeploy" || len(req.Project) == 0 {
		return "", errors.New("invalid project deploy hook invocation")
	}
	capability, err := r.describeSnapshot(ctx, path, req.Step.Package)
	// Only the snapshotted binary may advertise its own deploy authority.
	if err != nil || !slices.Contains(capability.Hooks, workflow.ProjectDeployHook) {
		return "", ErrMissingDeployHook
	}
	return workflow.ProjectDeployHook, nil
}

// limitedOutput drains excessive output without retaining unbounded memory.
type limitedOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
}

// Write retains at most the protocol bound while allowing the process to terminate normally.
func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	// Discard overflow rather than allowing a blocked stdout pipe.
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.Buffer.Write(p)
	return n, nil
}

// snapshot checks installation permissions and returns a private executable copy of reviewed bytes.
func (r Runner) snapshot(name, expected string) (string, string, func(), error) {
	// Failed snapshots have no staged files to remove.
	noop := func() {}
	data, err := r.readPackage(name)
	// Missing or unsafe package installations are never executed.
	if err != nil {
		return "", "", noop, err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	// An approved package upgrade requires a fresh deployment plan.
	if expected != "" && expected != digest {
		return "", "", noop, errors.New("package changed after approval")
	}
	path, cleanup, err := r.stage(data)
	return path, digest, cleanup, err
}

// packagePath validates the administrator-controlled installation before reading executable bytes.
func (r Runner) packagePath(name string) (string, error) {
	// Project-supplied package names can never escape the installation directory.
	if !packageName.MatchString(name) {
		return "", errors.New("invalid package name")
	}
	directory, err := os.Lstat(r.Directory)
	// A writable or redirected installation directory invalidates all contained packages.
	if err != nil || !directory.IsDir() || directory.Mode().Perm()&0022 != 0 {
		return "", errors.New("unsafe package directory")
	}
	path := filepath.Join(r.Directory, name)
	info, err := os.Lstat(path)
	// Only ordinary executable files writable by their owner are trusted installations.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("package is missing or unsafe")
	}
	return path, nil
}

// readPackage bounds executable bytes after validating the protected installation path.
func (r Runner) readPackage(name string) ([]byte, error) {
	path, err := r.packagePath(name)
	// Refuse reads outside a validated administrator-controlled installation.
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	// Keep all reads bounded even if a file changes after metadata inspection.
	if err != nil {
		return nil, errors.New("cannot open package")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 128*1024*1024+1))
	// Bound package size before staging an executable snapshot.
	if err != nil || len(data) > 128*1024*1024 {
		return nil, errors.New("cannot read package")
	}
	return data, nil
}

// stage creates an owner-only executable snapshot rather than running mutable installed bytes.
func (r Runner) stage(data []byte) (string, func(), error) {
	// Failed staging has no remaining files to remove.
	noop := func() {}
	file, err := os.CreateTemp(r.Directory, ".run-")
	// Temporary copies stay in the protected installation directory.
	if err != nil {
		return "", noop, errors.New("cannot stage package")
	}
	// The caller always removes snapshots after process completion or cancellation.
	cleanup := func() { os.Remove(file.Name()) }
	_, writeErr := file.Write(data)
	modeErr := file.Chmod(0700)
	closeErr := file.Close()
	// Never execute partially staged bytes.
	if writeErr != nil || modeErr != nil || closeErr != nil {
		cleanup()
		return "", noop, errors.New("cannot stage package")
	}
	return file.Name(), cleanup, nil
}

// Snapshot returns a private immutable copy of a validated installed executable.
func (r Runner) Snapshot(name, expected string) (string, string, func(), error) {
	return r.snapshot(name, expected)
}

// planningTimeout bounds preparation independently of HTTP transport deadlines.
func (r Runner) planningTimeout() time.Duration {
	// Default to a bounded IaC preparation budget when not explicitly configured.
	if r.PlanTimeout <= 0 {
		return 10 * time.Minute
	}
	// A configured timeout cannot disable the hard administrator operation bound.
	if r.PlanTimeout > time.Hour {
		return time.Hour
	}
	return r.PlanTimeout
}
