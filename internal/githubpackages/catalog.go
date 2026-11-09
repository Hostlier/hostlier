package githubpackages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"hostlier/internal/packageexec"
)

// Catalog identifies the exact release whose advertised package artifacts are available.
type Catalog = packageexec.Catalog

// AvailablePackage retains compatibility with the shared source-independent catalog.
type AvailablePackage = packageexec.AvailablePackage

// Available lists release metadata and integrity identities without downloading executable content.
func (c *Client) Available(ctx context.Context, repository, version string) (Catalog, error) {
	metadata, err := c.loadRelease(ctx, repository, version)
	// A failed private repository lookup must never look like an empty catalog.
	if err != nil {
		return Catalog{}, err
	}
	manifest, err := c.catalogManifest(ctx, repository, metadata.Assets)
	// Unavailable checksum metadata cannot establish which older artifacts are installable.
	if err != nil {
		return Catalog{}, err
	}
	return releaseCatalog(repository, metadata, manifest), nil
}

// loadRelease uses latest only for discovery; installation still requires an explicit version.
func (c *Client) loadRelease(ctx context.Context, repository, version string) (release, error) {
	selection := version
	// A placeholder validates repository coordinates without requiring a tag for discovery.
	if selection == "" {
		selection = "latest"
	}
	// Reject malformed coordinates before constructing an authenticated URL.
	if err := ValidateSelection(repository, selection); err != nil {
		return release{}, err
	}
	endpoint := "/repos/" + repository + "/releases/latest"
	// Explicit tags never fall back to the latest release.
	if version != "" {
		endpoint = "/repos/" + repository + "/releases/tags/" + url.PathEscape(version)
	}
	raw, err := c.read(ctx, endpoint, "application/vnd.github+json", 4<<20)
	// API errors remain visible to the caller rather than becoming an empty list.
	if err != nil {
		return release{}, err
	}
	var metadata release
	// Only published metadata with a usable tag can describe installable artifacts.
	if json.Unmarshal(raw, &metadata) != nil || metadata.Draft || ValidateSelection(repository, metadata.Tag) != nil {
		return release{}, fmt.Errorf("invalid or unpublished GitHub release")
	}
	// Latest discovery excludes prereleases; an explicit tag may intentionally select one.
	if mismatchedRelease(metadata, version) {
		return release{}, fmt.Errorf("GitHub release does not match the requested version")
	}
	return metadata, nil
}

// mismatchedRelease keeps discovery and explicitly pinned selection unambiguous.
func mismatchedRelease(metadata release, version string) bool {
	return (version != "" && metadata.Tag != version) || (version == "" && metadata.Prerelease)
}

// catalogManifest downloads a small checksum file once when valid package candidates need it.
func (c *Client) catalogManifest(ctx context.Context, repository string, assets []asset) (string, error) {
	// Modern releases already carry integrity identities in their asset metadata.
	if !needsCatalogManifest(assets) {
		return "", nil
	}
	checksums, err := findAsset(assets, "SHA256SUMS")
	// A release with no manifest can still expose assets with their own valid digests.
	if err != nil {
		return "", nil
	}
	data, err := c.readAsset(ctx, repository, checksums, 1<<20)
	return string(data), err
}

// needsCatalogManifest avoids downloading even the checksum file when metadata is sufficient.
func needsCatalogManifest(assets []asset) bool {
	// Only supported package artifacts need integrity evidence for catalog inclusion.
	for _, candidate := range assets {
		_, _, ok := catalogIdentity(candidate)
		// A missing canonical digest may be supplied by an older checksum manifest.
		if ok && !metadataDigest(candidate) {
			return true
		}
	}
	return false
}

// metadataDigest recognizes the same canonical SHA-256 form used during installation.
func metadataDigest(candidate asset) bool {
	return strings.HasPrefix(candidate.Digest, "sha256:") && packageexec.ValidDigest(strings.TrimPrefix(candidate.Digest, "sha256:"))
}

// catalogIdentity parses platform suffixes while preserving hyphenated package names.
func catalogIdentity(candidate asset) (string, packageexec.Platform, bool) {
	// Invalid identities and impossible binary sizes cannot advertise installable content.
	if candidate.ID <= 0 || candidate.Size <= 0 || candidate.Size > packageexec.MaxBinaryBytes {
		return "", packageexec.Platform{}, false
	}
	parts := strings.Split(candidate.Name, "-")
	// The prefix and final two components identify the package release naming contract.
	if len(parts) < 4 || parts[0] != "hostlier" {
		return "", packageexec.Platform{}, false
	}
	target := packageexec.Platform{OS: parts[len(parts)-2], Arch: parts[len(parts)-1]}
	name := strings.Join(parts[1:len(parts)-2], "-")
	return name, target, packageexec.ValidName(name) && supported(target)
}

// catalogCandidate rejects ambiguous names and assets without install-time integrity evidence.
func catalogCandidate(candidate asset, assets []asset, manifest string) bool {
	// Installation also rejects duplicate release filenames.
	if _, err := findAsset(assets, candidate.Name); err != nil {
		return false
	}
	// Provider metadata takes precedence over optional checksum manifests.
	if metadataDigest(candidate) {
		return true
	}
	_, err := checksumFor(manifest, candidate.Name)
	return err == nil
}

// releaseCatalog groups usable advertised assets deterministically without executing their contents.
func releaseCatalog(repository string, metadata release, manifest string) Catalog {
	byName := map[string][]packageexec.Platform{}
	// Ignore source archives, checksum files, unsupported targets and malformed package artifacts.
	for _, candidate := range metadata.Assets {
		name, target, ok := catalogIdentity(candidate)
		// Only candidates the installer can select with integrity evidence belong in this catalog.
		if !ok || !catalogCandidate(candidate, metadata.Assets, manifest) {
			continue
		}
		byName[name] = append(byName[name], target)
	}
	result := Catalog{Repository: repository, Version: metadata.Tag, Packages: []AvailablePackage{}}
	// Stable package and platform ordering makes command output readable and scriptable.
	for name, platforms := range byName {
		// Sort platform tuples independently of GitHub upload order.
		sort.Slice(platforms, func(i, j int) bool {
			return platforms[i].OS+"/"+platforms[i].Arch < platforms[j].OS+"/"+platforms[j].Arch
		})
		result.Packages = append(result.Packages, AvailablePackage{Name: name, Platforms: platforms})
	}
	// Stable package ordering avoids map iteration leaking into CLI output.
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].Name < result.Packages[j].Name })
	return result
}
