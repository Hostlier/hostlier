package artifact

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"hostlier/internal/model"
)

// RegistryImage describes a registry download, independently of the deployment file's local build sources.
type RegistryImage struct {
	Image        string `json:"image"`
	Architecture string `json:"architecture,omitempty"`
}

// DownloadRegistry archives exactly one Linux platform without requiring a container daemon.
func DownloadRegistry(ctx context.Context, source RegistryImage, path string, auth authn.Authenticator, transport http.RoundTripper, reserve *Store) error {
	ref, err := name.ParseReference(source.Image)
	// Reject credential-bearing URLs and malformed references before network access.
	if err != nil {
		return model.Err("INVALID_CONFIG", "Container image must be a valid registry image reference")
	}
	image, err := remote.Image(ref, remote.WithContext(ctx), remote.WithAuth(auth),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: source.Architecture}), remote.WithTransport(transport))
	// Raw registry diagnostics may contain credentials or signed download URLs.
	if err != nil {
		return registryDownloadError(ctx, err)
	}
	config, err := image.ConfigFile()
	// Validate actual config even for single-platform manifests and digest pins.
	if err != nil {
		return registryDownloadError(ctx, err)
	}
	// Index labels cannot substitute for the platform in the runnable image config.
	if config.OS != "linux" || config.Architecture != source.Architecture {
		return model.Err("ARTIFACT_PLATFORM", "Registry image does not match the requested Linux architecture")
	}
	tag, _ := name.NewTag("hostlier.local/staging:downloaded")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	// Downloads never overwrite an existing archive or publish partial content.
	if err != nil {
		return registryDownloadError(ctx, err)
	}
	defer file.Close()
	// Consume all lazy layer readers before publication and omit the registry's original tag.
	if err := tarball.Write(tag, image, registryArchiveWriter{Writer: file, reserve: reserve}); err != nil {
		return registryDownloadError(ctx, err)
	}
	return registryDownloadError(ctx, file.Close())
}

// registryArchiveWriter preserves the controller's filesystem reserve while streaming a large download.
type registryArchiveWriter struct {
	io.Writer
	reserve *Store
}

// Write checks capacity before each filesystem append; laptop downloads retain ordinary local disk semantics.
func (w registryArchiveWriter) Write(data []byte) (int, error) {
	// Only controller-side preparation needs the protected artifact filesystem reserve.
	if w.reserve != nil {
		if err := w.reserve.CheckAvailableSpace(int64(len(data))); err != nil { // Never deplete the system reserve mid-download.
			return 0, err
		}
	}
	return w.Writer.Write(data)
}

// registryDownloadError keeps local disk failures distinct from registry access failures eligible for fallback.
func registryDownloadError(ctx context.Context, cause error) error {
	// Closing a completely written archive is a normal successful result.
	if cause == nil {
		return nil
	}
	// Request cancellation must stop preparation rather than initiate a second download.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *model.Error
	// Disk-reserve failures must stop preparation rather than trigger a laptop upload to the same full disk.
	if errors.As(cause, &failure) && failure.Code == "ARTIFACT_SPACE" {
		return failure
	}
	var pathError *os.PathError
	// A different registry credential cannot fix an unwritable or full preparation filesystem.
	if errors.As(cause, &pathError) {
		return model.Err("ARTIFACT_WRITE", "Cannot save downloaded image; check temporary disk space and permissions")
	}
	return model.Err("IMAGE_PULL", "Registry image download failed; check registry access, image and requested Linux platform")
}
