package packageexec

import (
	"context"
	"os"
	"time"

	"hostlier/pkg/packagekit"
)

// Init uses the same pinned capability negotiation and bounded pipes as the other local hooks.
func (hooks *ProjectHooks) Init(ctx context.Context, req packagekit.InitRequest) (packagekit.InitResponse, error) {
	var result packagekit.InitResponse
	// Init accepts only the app setup inputs and never sees build credentials.
	if err := hooks.invoke(ctx, "init", req, &result, 30*time.Second); err != nil {
		return result, err
	}
	return result, hooks.response("init", result.Version, result.Error)
}

// localSnapshot works with read-only CLI releases as well as administrator-installed binaries.
func (r Runner) localSnapshot(name string) (string, func(), error) {
	data, err := r.readPackage(name)
	// Refuse to stage bytes from an untrusted installation.
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "hostlier-local-package-")
	// Temporary files never need write access to the package installation.
	if err != nil {
		return "", nil, err
	}
	path, _, err := (Runner{Directory: dir}).stage(data)
	// All files in this directory belong to this invocation.
	cleanup := func() { _ = os.RemoveAll(dir) }
	// Staging failures do not transfer cleanup ownership.
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}
