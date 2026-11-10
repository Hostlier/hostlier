package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"hostlier/internal/artifact"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// pullContainerArchive downloads one platform using the invoking user's Docker login, without a daemon.
func pullContainerArchive(ctx context.Context, source artifact.RegistryImage, path string, transport http.RoundTripper) error {
	ref, err := name.ParseReference(source.Image)
	// Invalid references must fail before reading credentials or making network requests.
	if err != nil {
		return model.Err("INVALID_CONFIG", "Container image must be a valid registry image reference")
	}
	// Resolve the registry itself so credHelpers takes precedence over the global store.
	// Resolving a repository first can incorrectly invoke credsStore for a different key.
	auth, err := authn.Resolve(ctx, authn.DefaultKeychain, ref.Context().Registry)
	// Credential helpers and config errors may contain secrets in their diagnostics.
	if err != nil {
		return containerPullError(ctx)
	}
	return artifact.DownloadRegistry(ctx, source, path, auth, transport, nil)
}

// prepareRegistryImage tries the controller first and uploads locally authenticated content only on registry failure.
func (s *session) prepareRegistryImage(image string) (artifact.Result, error) {
	// Validate registry syntax before receiver discovery or local credential lookup.
	if err := config.ValidateRegistryImage(image, ""); err != nil {
		return artifact.Result{}, err
	}
	architecture, err := s.resolveContainerArchitecture("")
	// Missing or untrusted receiver information cannot select a laptop-native image.
	if err != nil {
		return artifact.Result{}, err
	}
	source := artifact.RegistryImage{Image: image, Architecture: architecture}
	receipt, fallback, err := s.prepareControllerImage(source)
	// Controller trust, storage and platform failures must not become a second credential attempt.
	if err != nil || !fallback {
		return receipt, err
	}
	dir, err := os.MkdirTemp("", "hostlier-container-")
	// Failed preparation cannot share temporary bytes with another command.
	if err != nil {
		return artifact.Result{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "registry.tar")
	fmt.Fprintf(s.errOut, "Downloading %s locally for linux/%s\n", image, architecture)
	// A failed download cannot upload partial content or substitute an old image.
	if err := pullContainerArchive(s.ctx, source, path, remote.DefaultTransport); err != nil {
		return artifact.Result{}, err
	}
	return s.uploadContainerArchive(dir, path, architecture)
}

// containerPullError excludes registry response bodies, token-service URLs and credential-helper output.
func containerPullError(ctx context.Context) error {
	// Preserve cancellation so callers stop their deployment instead of retrying authentication.
	if err := ctx.Err(); err != nil {
		return err
	}
	return model.Err("IMAGE_PULL", "Local registry image download failed; check the image, requested Linux platform and local Docker login (including DOCKER_CONFIG and credential helpers)")
}
