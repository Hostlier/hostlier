package cli

import (
	"context"
	"path/filepath"
	goruntime "runtime"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"hostlier/internal/artifact"
)

// prepareRegistryFixture supplies real verified image bytes without contacting a registry or Docker daemon.
func prepareRegistryFixture(ctx context.Context, dir string) (artifact.Result, error) {
	// Integration fixtures respect cancellation just like a real registry preparation.
	if err := ctx.Err(); err != nil {
		return artifact.Result{}, err
	}
	image, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: goruntime.GOARCH})
	// Runnable config metadata must match the fixture controller's discovered architecture.
	if err != nil {
		return artifact.Result{}, err
	}
	tag, _ := name.NewTag("local/fixture:registry")
	path := filepath.Join(dir, "fixture.tar")
	// Native preparation must return a complete archive that the controller independently verifies.
	if err := tarball.WriteToFile(path, tag, image); err != nil {
		return artifact.Result{}, err
	}
	return artifact.PrepareContainer(dir, path, goruntime.GOARCH)
}

// PrepareRegistryImage lets executable approval tests exercise the actual controller-first staging API.
func (*recordingRuntime) PrepareRegistryImage(ctx context.Context, _ string, dir string) (artifact.Result, error) {
	return prepareRegistryFixture(ctx, dir)
}

// PrepareRegistryImage keeps workflow/product fixtures on the ordinary image preparation path.
func (*workflowEngine) PrepareRegistryImage(ctx context.Context, _ string, dir string) (artifact.Result, error) {
	return prepareRegistryFixture(ctx, dir)
}
