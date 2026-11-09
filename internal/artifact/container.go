package artifact

import (
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"hostlier/internal/model"
	"os"
)

// ContainerImage validates a single portable Docker archive without extracting or executing it.
func ContainerImage(path, architecture string) (v1.Image, error) {
	// Explicit supported platforms prevent accidental laptop-native images.
	if architecture != "amd64" && architecture != "arm64" {
		return nil, model.Err("ARTIFACT_PLATFORM", "Container architecture must be amd64 or arm64")
	}
	// Pipes and devices cannot enter the local preparation path.
	if err := validateContainerArchive(path); err != nil {
		return nil, err
	}
	image, err := tarball.ImageFromPath(path, nil)
	// Multi-image or malformed exports require an explicit single-image Docker save.
	if err != nil {
		return nil, model.Err("ARTIFACT_IMAGE", "Expected a single-image Docker archive; export it with docker save")
	}
	config, err := image.ConfigFile()
	// The manifest's actual platform must match the authored target.
	if err != nil || config.OS != "linux" || config.Architecture != architecture {
		return nil, model.Err("ARTIFACT_PLATFORM", "Container archive does not match the requested Linux architecture")
	}
	return image, nil
}

// validateContainerArchive requires a nonempty regular file before metadata is parsed.
func validateContainerArchive(path string) error {
	info, err := os.Stat(path)
	// Only nonempty regular files can supply portable container content.
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return model.Err("ARTIFACT_LIMIT", "Container archive must be a nonempty regular file")
	}
	return nil
}

// PrepareContainer retags and hashes portable content for the existing verified upload protocol.
func PrepareContainer(dir, path, architecture string) (Result, error) {
	image, err := ContainerImage(path, architecture)
	// Invalid images must not generate upload receipts or consume server storage.
	if err != nil {
		return Result{}, err
	}
	return writeImage(dir, image)
}
