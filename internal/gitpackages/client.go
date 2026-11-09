package gitpackages

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"hostlier/internal/packageexec"
)

// Client uses local Git authentication and a separately scoped artifact transport.
type Client struct {
	Environment []string
	HTTP        *http.Client
}

// New creates a secure bounded artifact client; Git uses the caller's credential helpers and SSH agent.
func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: artifactRedirect}}
}

// artifactRedirect never forwards authorization to a redirected artifact location.
func artifactRedirect(request *http.Request, previous []*http.Request) error {
	// Redirects must retain secure transport and cannot embed new credentials.
	if request.URL.Scheme != "https" || request.URL.User != nil || len(previous) >= 5 {
		return fmt.Errorf("unsafe package download redirect")
	}
	request.Header.Del("Authorization")
	return nil
}

// Available reads the repository catalog at HEAD or a selected ref and returns its resolved commit.
func (c *Client) Available(ctx context.Context, repository, ref string) (packageexec.Catalog, error) {
	repo, err := c.open(ctx, repository, ref)
	// Missing refs and authentication failures must not become empty catalogs.
	if err != nil {
		return packageexec.Catalog{}, err
	}
	defer os.RemoveAll(repo.directory)
	manifest, err := repo.manifest(ctx)
	// Discovery never checks out source files or downloads external executables.
	if err != nil {
		return packageexec.Catalog{}, err
	}
	return manifest.catalog(repository, repo.commit), nil
}

// Package resolves a source ref once, then verifies exactly the selected native binary.
func (c *Client) Package(ctx context.Context, repository, ref, name string, target packageexec.Platform) (packageexec.Download, error) {
	// Installation must explicitly select a version even though discovery may follow HEAD.
	if ref == "" || !packageexec.ValidName(name) {
		return packageexec.Download{}, fmt.Errorf("package installation requires a name and explicit --version TAG_OR_COMMIT")
	}
	repo, err := c.open(ctx, repository, ref)
	// Never fall back to a moving branch after a failed pinned fetch.
	if err != nil {
		return packageexec.Download{}, err
	}
	defer os.RemoveAll(repo.directory)
	manifest, err := repo.manifest(ctx)
	// A source repository must explicitly advertise its executable artifacts.
	if err != nil {
		return packageexec.Download{}, err
	}
	artifact, err := manifest.selectArtifact(name, target)
	// Missing architectures must fail before downloading or staging any executable.
	if err != nil {
		return packageexec.Download{}, err
	}
	return c.download(ctx, repo, repository, name, artifact, target)
}

// manifest reads only a small regular catalog blob from the resolved commit.
func (r *checkout) manifest(ctx context.Context) (Manifest, error) {
	data, err := r.blob(ctx, "hostlier-packages.json", 1<<20)
	// Arbitrary repositories need the explicit catalog contract before exposing installable packages.
	if err != nil {
		return Manifest{}, fmt.Errorf("cannot read hostlier-packages.json from the selected Git commit")
	}
	return decodeManifest(data)
}

// download verifies the publisher's checksum and actual native header without executing repository code.
func (c *Client) download(ctx context.Context, repo *checkout, repository, name string, artifact Artifact, target packageexec.Platform) (packageexec.Download, error) {
	var data []byte
	var err error
	// Tracked binaries are read from the same immutable tree as their manifest.
	if artifact.Path != "" {
		data, err = repo.blob(ctx, artifact.Path, packageexec.MaxBinaryBytes)
	} else {
		data, err = c.downloadURL(ctx, artifact.URL)
	}
	// Incomplete transfers cannot become installation requests.
	if err != nil {
		return packageexec.Download{}, err
	}
	// The catalog checksum applies to both Git blobs and separately hosted binaries.
	if len(data) == 0 || packageexec.Digest(data) != artifact.SHA256 {
		return packageexec.Download{}, fmt.Errorf("package artifact checksum mismatch")
	}
	// Filenames and manifest claims cannot substitute for native format inspection.
	if err = packageexec.CheckPlatform(data, target); err != nil {
		return packageexec.Download{}, err
	}
	request := packageexec.InstallRequest{Name: name, Digest: artifact.SHA256, Size: int64(len(data)), Source: packageexec.Source{Repository: repository, Version: repo.commit, Asset: name + "-" + target.OS + "-" + target.Arch}}
	return packageexec.Download{Data: data, Request: request}, nil
}

// downloadURL reads a bounded HTTPS artifact without exposing URL query strings in errors or reviews.
func (c *Client) downloadURL(ctx context.Context, source string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", source, nil)
	// Manifest validation must precede issuing any artifact request.
	if err != nil || !safeArtifactURL(source) {
		return nil, fmt.Errorf("invalid package artifact URL")
	}
	request.Header.Set("Accept", "application/octet-stream")
	response, err := c.HTTP.Do(request)
	// Transport diagnostics can include signed URL credentials and remain private.
	if err != nil {
		return nil, fmt.Errorf("package artifact download failed")
	}
	defer response.Body.Close()
	// Provider error bodies never belong in controller approval metadata.
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("package artifact returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, packageexec.MaxBinaryBytes+1))
	// Transfers must remain within the server's executable size bound.
	if err != nil || len(data) > packageexec.MaxBinaryBytes {
		return nil, fmt.Errorf("package artifact incomplete or too large")
	}
	return data, nil
}
