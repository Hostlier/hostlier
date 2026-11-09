package gitpackages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"

	"hostlier/internal/packageexec"
)

// Manifest is the provider-independent catalog stored at the repository root.
type Manifest struct {
	Version  int       `json:"version"`
	Packages []Package `json:"packages"`
}

// Package lists explicitly published native binaries for a package implementation.
type Package struct {
	Name      string     `json:"name"`
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact pins either a tracked Git blob or an HTTPS download to its native platform and SHA-256.
type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Path   string `json:"path,omitempty"`
	URL    string `json:"url,omitempty"`
	SHA256 string `json:"sha256"`
}

// decodeManifest rejects ambiguous schemas, duplicate package names and invalid executable identities.
func decodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	// Catalog evolution requires an explicit supported schema version.
	if decoder.Decode(&manifest) != nil || manifest.Version != 1 || decoder.Decode(new(any)) != io.EOF {
		return manifest, fmt.Errorf("invalid hostlier-packages.json catalog; expected schema version 1")
	}
	seen := map[string]bool{}
	// Package identity must be unique within the pinned catalog.
	for _, item := range manifest.Packages {
		// Duplicate names cannot select a different implementation depending on iteration order.
		if !packageexec.ValidName(item.Name) || seen[item.Name] {
			return manifest, fmt.Errorf("invalid or duplicate catalog package name")
		}
		seen[item.Name] = true
		// Every advertised platform must have one complete binary location and integrity identity.
		if err := validateArtifacts(item.Artifacts); err != nil {
			return manifest, err
		}
	}
	return manifest, nil
}

// validateArtifacts prevents ambiguous platform selection and locations outside the supported transfer model.
func validateArtifacts(artifacts []Artifact) error {
	seen := map[string]bool{}
	// One package version can contain at most one artifact for each native target.
	for _, a := range artifacts {
		target := a.OS + "/" + a.Arch
		// Check integrity independently from the untrusted filename or location.
		if !supportedArtifact(a) || !packageexec.ValidDigest(a.SHA256) || seen[target] {
			return fmt.Errorf("invalid or duplicate catalog artifact")
		}
		seen[target] = true
		// A binary must come from exactly one bounded source.
		if (a.Path == "") == (a.URL == "") {
			return fmt.Errorf("catalog artifact requires exactly one path or url")
		}
		// Blob paths are relative to the pinned Git tree and cannot contain revision expressions.
		if a.Path != "" && !safePath(a.Path) {
			return fmt.Errorf("invalid catalog artifact path")
		}
		// Remote artifacts require HTTPS independently of the Git repository's transport.
		if a.URL != "" && !safeArtifactURL(a.URL) {
			return fmt.Errorf("catalog artifacts require HTTPS URLs without embedded credentials")
		}
	}
	return nil
}

// supportedArtifact matches the native executable formats validated by the installer.
func supportedArtifact(a Artifact) bool {
	return (a.OS == "linux" || a.OS == "darwin") && (a.Arch == "amd64" || a.Arch == "arm64")
}

// safePath accepts a literal relative Git-tree path without traversal or Git expression syntax.
func safePath(value string) bool {
	return len(value) > 0 && len(value) <= 250 && value != "." && value != ".." && path.Clean(value) == value && !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, ":\\\x00\r\n\t*?[")
}

// safeArtifactURL permits secure downloads without credentials in source metadata.
func safeArtifactURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == "" && len(value) <= 4096
}

// catalog emits the resolved commit and deterministic platform inventories without downloading artifacts.
func (m Manifest) catalog(repository, commit string) packageexec.Catalog {
	result := packageexec.Catalog{Repository: repository, Version: commit, Packages: []packageexec.AvailablePackage{}}
	// Source-only entries have no installable platform and are omitted from discovery.
	for _, item := range m.Packages {
		// Only packages with advertised executable artifacts can be installed.
		if len(item.Artifacts) == 0 {
			continue
		}
		available := packageexec.AvailablePackage{Name: item.Name, Platforms: []packageexec.Platform{}}
		// Platform values have already passed schema and duplicate validation.
		for _, a := range item.Artifacts {
			available.Platforms = append(available.Platforms, packageexec.Platform{OS: a.OS, Arch: a.Arch})
		}
		// Output ordering must not depend on the publisher's manifest ordering.
		sort.Slice(available.Platforms, func(i, j int) bool {
			return available.Platforms[i].OS+available.Platforms[i].Arch < available.Platforms[j].OS+available.Platforms[j].Arch
		})
		result.Packages = append(result.Packages, available)
	}
	// A stable name order makes source discovery predictable for people and scripts.
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].Name < result.Packages[j].Name })
	return result
}

// selectArtifact requires one exact package name and server target from the pinned catalog.
func (m Manifest) selectArtifact(name string, target packageexec.Platform) (Artifact, error) {
	// Package names are data, never paths or executable build commands.
	for _, item := range m.Packages {
		// Other packages cannot provide implicit fallback implementations.
		if item.Name != name {
			continue
		}
		// The server's actual target selects its advertised native binary.
		for _, a := range item.Artifacts {
			// Exact platform matching prevents accidental laptop-architecture installation.
			if a.OS == target.OS && a.Arch == target.Arch {
				return a, nil
			}
		}
	}
	return Artifact{}, fmt.Errorf("catalog has no %s package for %s/%s", name, target.OS, target.Arch)
}
