package runtime

import (
	"context"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// artifactStore locates content retained alongside controller deployment revisions.
func (r *Runtime) artifactStore() artifact.Store {
	return artifact.Store{Root: filepath.Join(r.options.StateDir, "artifacts")}
}

// inspectArtifact validates retained image identity before approval.
func (r *Runtime) inspectArtifact(image string) (model.ImageInfo, error) {
	receipt, err := r.artifactStore().Inspect(image)
	// Platform mismatch must be rejected before approval.
	if err == nil {
		err = artifactPlatform(receipt)
	}
	return model.ImageInfo{Image: receipt.Image, Digest: receipt.Image}, err
}

// artifactPlatform rejects images built for a different server architecture.
func artifactPlatform(receipt artifact.Result) error {
	image, err := tarball.ImageFromPath(receipt.Path, nil)
	// A corrupt archive must not be passed to a privileged importer.
	if err != nil {
		return err
	}
	config, err := image.ConfigFile()
	// Explicit platform checks avoid silent emulation or wrong-architecture launches.
	if err != nil || config.OS != "linux" || config.Architecture != runtime.GOARCH {
		return model.Err("ARTIFACT_PLATFORM", "Prepared artifact does not target this Linux server architecture")
	}
	return nil
}

// importArtifact streams a verified archive into Docker without invoking a build.
func (r *Runtime) importArtifact(ctx context.Context, image string) error {
	receipt, err := r.artifactStore().Inspect(image)
	// Rehash the retained payload before crossing the daemon privilege boundary.
	if err != nil {
		return err
	}
	// Wrong-platform uploads cannot become runnable workloads.
	if err = artifactPlatform(receipt); err != nil {
		return err
	}
	f, err := os.Open(receipt.Path)
	// Import only the protected retained archive.
	if err != nil {
		return err
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docker/images/load?quiet=1", f)
	// Preserve request setup failures before accessing the daemon.
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	req.ContentLength = receipt.Size
	response, err := r.docker.Do(req)
	// No daemon response means no confirmed import.
	if err != nil {
		return err
	}
	defer response.Body.Close()
	// Bound diagnostic output and never expose archive or daemon payloads.
	if response.StatusCode != http.StatusOK {
		return model.Err("ARTIFACT_IMPORT", "Docker image import failed")
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1024*1024))
	// Configuration inspection below confirms the runnable object after streamed load.
	if err != nil {
		return err
	}
	var installed struct {
		ID string `json:"Id"`
	}
	err = r.api(ctx, "GET", "/images/"+url.PathEscape(artifact.RuntimeImage(image))+"/json", nil, &installed, nil)
	// A preexisting or substituted tag cannot stand in for approved content.
	if err != nil {
		return model.Err("ARTIFACT_IMPORT", "Imported Docker image unavailable")
	}
	// Classic Docker exposes the config digest directly.
	if installed.ID == receipt.ConfigDigest {
		return nil
	}
	return r.verifyImportedExport(ctx, receipt)
}

// importGroupArtifacts loads precompiled images directly into k3s containerd.
func (k *K3s) importGroupArtifacts(ctx context.Context, p model.Project) error {
	images := []string{p.Image}
	// Every sidecar has the same artifact support as the primary workload.
	for _, m := range p.Sidecars {
		images = append(images, m.Image)
	}
	// Registry images continue through the normal kubelet image-pull path.
	for _, image := range images {
		// Import only images that came through the verified artifact store.
		if !artifact.IsImage(image) {
			continue
		}
		receipt, err := k.base.artifactStore().Inspect(image)
		// Missing or modified content must not trigger an external registry fallback.
		if err != nil {
			return err
		}
		// The initial backend is a single local k3s server with a known architecture.
		if err = artifactPlatform(receipt); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, k.options.KubeBinary, "ctr", "-n", "k8s.io", "images", "import", "--digests", receipt.Path)
		// Import never invokes Docker Engine or executes the artifact.
		if err = cmd.Run(); err != nil {
			return model.Err("ARTIFACT_IMPORT", "k3s containerd image import failed")
		}
	}
	return nil
}

// verifyImportedExport supports Docker containerd stores whose inspect ID identifies a manifest.
func (r *Runtime) verifyImportedExport(ctx context.Context, receipt artifact.Result) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/images/"+url.PathEscape(artifact.RuntimeImage(receipt.Image))+"/get", nil)
	// Request construction must succeed before using the privileged daemon connection.
	if err != nil {
		return err
	}
	response, err := r.docker.Do(req)
	// An unavailable export cannot confirm a tag's runnable identity.
	if err != nil {
		return err
	}
	defer response.Body.Close()
	// Only a successful Docker export is a valid verification source.
	if response.StatusCode != http.StatusOK {
		return model.Err("ARTIFACT_IMPORT", "Cannot verify imported image")
	}
	return artifact.VerifyExport(response.Body, receipt)
}
