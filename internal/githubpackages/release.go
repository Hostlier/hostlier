package githubpackages

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"hostlier/internal/packageexec"
)

// asset contains only immutable API identity and bounded release metadata used for selection.
type asset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// release excludes descriptions and arbitrary download URLs from executable selection.
type release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

// Download carries verified native bytes and public provenance only.
type Download = packageexec.Download

// Package selects the receiver's exact platform and verifies a release checksum before returning bytes.
func (c *Client) Package(ctx context.Context, repository, version, name string, target packageexec.Platform) (Download, error) {
	// Invalid source coordinates must fail before contacting a repository.
	if err := ValidateSelection(repository, version); err != nil {
		return Download{}, err
	}
	// Names and target identities form a single exact release asset name.
	if !packageexec.ValidName(name) || !supported(target) {
		return Download{}, fmt.Errorf("unsupported package name or platform")
	}
	metadata, err := c.loadRelease(ctx, repository, version)
	// Source lookup failure cannot select another release.
	if err != nil {
		return Download{}, err
	}

	return c.releasePackage(ctx, repository, name, target, metadata)
}

// supported keeps executable targets aligned with native formats verified by the installer.
func supported(target packageexec.Platform) bool {
	return (target.OS == "linux" || target.OS == "darwin") && (target.Arch == "amd64" || target.Arch == "arm64")
}

// releasePackage validates the selected asset, checksum and native header without executing it.
func (c *Client) releasePackage(ctx context.Context, repository, name string, target packageexec.Platform, metadata release) (Download, error) {
	filename := "hostlier-" + name + "-" + target.OS + "-" + target.Arch
	selected, err := findAsset(metadata.Assets, filename)
	// Missing architecture-specific artifacts require a matching release from the publisher.
	if err != nil {
		return Download{}, err
	}
	// Reject impossible metadata before downloading large content.
	if selected.Size <= 0 || selected.Size > packageexec.MaxBinaryBytes {
		return Download{}, fmt.Errorf("package release asset exceeds size limit")
	}
	digest, err := c.assetDigest(ctx, repository, metadata.Assets, selected)
	// A checksum is required even when the caller has authenticated GitHub access.
	if err != nil {
		return Download{}, err
	}
	data, err := c.readAsset(ctx, repository, selected, packageexec.MaxBinaryBytes)
	// The provider size and checksum bind the exact downloaded bytes.
	if err != nil {
		return Download{}, err
	}
	// Truncation and checksum mismatches cannot reach staging.
	if int64(len(data)) != selected.Size || packageexec.Digest(data) != digest {
		return Download{}, fmt.Errorf("GitHub package checksum or size mismatch")
	}
	// Header inspection prevents a platform label from installing incompatible content.
	if err = packageexec.CheckPlatform(data, target); err != nil {
		return Download{}, err
	}
	return Download{Data: data, Request: packageexec.InstallRequest{Name: name, Digest: digest, Size: int64(len(data)), Source: packageexec.Source{Repository: repository, Version: metadata.Tag, Asset: filename}}}, nil
}

// findAsset requires one unambiguous uploaded release asset with an API identity.
func findAsset(assets []asset, name string) (asset, error) {
	var selected asset
	// Duplicate names are ambiguous even if one happens to appear first.
	for _, candidate := range assets {
		// Match literal names only; patterns could select unrelated release files.
		if candidate.Name == name {
			// Asset identifiers are supplied by GitHub, never by arbitrary URLs in a manifest.
			if selected.ID != 0 || candidate.ID <= 0 {
				return asset{}, fmt.Errorf("ambiguous release asset %s", name)
			}
			selected = candidate
		}
	}
	// Missing assets must not fall back to another architecture or source archive.
	if selected.ID == 0 {
		return asset{}, fmt.Errorf("release has no %s asset", name)
	}
	return selected, nil
}

// readAsset selects the trusted API endpoint, never a caller-supplied download URL.
func (c *Client) readAsset(ctx context.Context, repository string, selected asset, limit int64) ([]byte, error) {
	return c.read(ctx, "/repos/"+repository+"/releases/assets/"+strconv.FormatInt(selected.ID, 10), "application/octet-stream", limit)
}

// assetDigest prefers GitHub's SHA-256 metadata and supports older releases with SHA256SUMS.
func (c *Client) assetDigest(ctx context.Context, repository string, assets []asset, selected asset) (string, error) {
	digest := strings.TrimPrefix(selected.Digest, "sha256:")
	// Accept only a canonical explicitly identified SHA-256 digest from the authenticated API.
	if strings.HasPrefix(selected.Digest, "sha256:") && packageexec.ValidDigest(digest) {
		return digest, nil
	}
	checksums, err := findAsset(assets, "SHA256SUMS")
	// Releases without integrity metadata cannot be installed.
	if err != nil {
		return "", fmt.Errorf("release requires SHA-256 metadata or SHA256SUMS")
	}
	data, err := c.readAsset(ctx, repository, checksums, 1<<20)
	// Checksum download failure cannot waive integrity validation.
	if err != nil {
		return "", err
	}
	return checksumFor(string(data), selected.Name)
}

// checksumFor accepts standard sha256sum lines and rejects duplicate or malformed matching identities.
func checksumFor(manifest, name string) (string, error) {
	digest := ""
	// Each checksum entry is interpreted as data, never as a local path.
	for _, line := range strings.Split(manifest, "\n") {
		fields := strings.Fields(line)
		// Unrelated files have no authority over the selected executable.
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		// Ambiguous or invalid entries fail closed.
		if digest != "" || !packageexec.ValidDigest(fields[0]) {
			return "", fmt.Errorf("invalid release checksum")
		}
		digest = fields[0]
	}
	// A manifest must explicitly name the selected artifact.
	if digest == "" {
		return "", fmt.Errorf("package missing from SHA256SUMS")
	}
	return digest, nil
}
