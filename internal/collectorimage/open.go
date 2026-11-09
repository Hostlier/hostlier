package collectorimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

const maxMetadata = 1 << 20
const maxBlob = 256 << 20

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Open validates a trusted, root-owned release layout and retains immutable blobs.
// Digest checks detect corruption; authenticity comes from the trusted installer source.
func Open(directory string) (*Image, error) {
	raw, err := readRegular(filepath.Join(directory, "oci-layout"), maxMetadata)
	// Reject missing or incompatible layout versions before opening any blobs.
	if err != nil {
		return nil, err
	}
	var layout struct {
		Version string `json:"imageLayoutVersion"`
	}
	// The layout format is fixed rather than inferred from directory contents.
	if json.Unmarshal(raw, &layout) != nil || layout.Version != "1.0.0" {
		return nil, fmt.Errorf("invalid OCI layout version")
	}
	raw, err = readRegular(filepath.Join(directory, "index.json"), maxMetadata)
	// The index is the release's atomic publication point.
	if err != nil {
		return nil, err
	}
	i := &Image{index: raw, digest: digestBytes(raw), blobs: map[string]blob{}, manifests: map[string]bool{}}
	// Close descriptors if any referenced object fails validation.
	if err = i.loadIndex(directory); err != nil {
		i.Close()
		return nil, err
	}
	return i, nil
}

// loadIndex restricts the release to supported, unique Linux platform manifests.
func (i *Image) loadIndex(directory string) error {
	var index imageIndex
	// Bound metadata fanout and reject incompatible schema versions.
	if json.Unmarshal(i.index, &index) != nil || index.SchemaVersion != 2 || index.MediaType != indexMediaType || len(index.Manifests) == 0 || len(index.Manifests) > 2 {
		return fmt.Errorf("invalid collector image index")
	}
	seen := map[string]bool{}
	// Validate every platform before making the registry handler available.
	for _, d := range index.Manifests {
		// Only explicit Linux platforms can match the distributed executables.
		if !validPlatform(d.Platform) || seen[d.Platform.Architecture] {
			return fmt.Errorf("invalid or duplicate collector platform")
		}
		seen[d.Platform.Architecture] = true
		// Each manifest must resolve to the declared configuration and layer.
		if err := i.loadManifest(directory, d); err != nil {
			return err
		}
		i.platforms = append(i.platforms, *d.Platform)
	}
	return nil
}

// validPlatform enumerates the release architectures instead of accepting arbitrary metadata.
func validPlatform(p *Platform) bool {
	return p != nil && p.OS == "linux" && (p.Architecture == "arm64" || p.Architecture == "amd64")
}

// loadManifest checks platform identity and all transitive content descriptors.
func (i *Image) loadManifest(directory string, d descriptor) error {
	raw, err := i.loadJSON(directory, d, manifestMediaType)
	// Invalid manifest bytes never become servable content.
	if err != nil {
		return err
	}
	var m manifest
	// A scratch collector has exactly one bounded gzip filesystem layer.
	if json.Unmarshal(raw, &m) != nil || m.SchemaVersion != 2 || m.MediaType != manifestMediaType || len(m.Layers) != 1 {
		return fmt.Errorf("invalid collector image manifest")
	}
	config, err := i.loadJSON(directory, m.Config, configMediaType)
	// Configuration identity must agree with the index platform.
	if err != nil {
		return err
	}
	var p Platform
	// Prevent image selection from delivering a binary for a different node architecture.
	if json.Unmarshal(config, &p) != nil || p != *d.Platform {
		return fmt.Errorf("collector image platform mismatch")
	}
	// The only large object accepted is the fixed compressed root filesystem.
	if err = i.loadBlob(directory, m.Layers[0], layerMediaType); err != nil {
		return err
	}
	i.manifests[d.Digest] = true
	return nil
}

// loadJSON applies a small metadata limit before retaining a content-addressed object.
func (i *Image) loadJSON(directory string, d descriptor, media string) ([]byte, error) {
	// Metadata cannot claim the larger filesystem-layer budget.
	if d.Size > maxMetadata {
		return nil, fmt.Errorf("collector metadata exceeds limit")
	}
	// A verified blob is safe to read from its retained descriptor.
	if err := i.loadBlob(directory, d, media); err != nil {
		return nil, err
	}
	b := i.blobs[d.Digest]
	return io.ReadAll(io.NewSectionReader(b.file, 0, b.size))
}

// loadBlob hashes exact file bytes and rejects symlinks, missing data and size mismatches.
func (i *Image) loadBlob(directory string, d descriptor, media string) error {
	// Descriptor values must never become unconstrained filesystem paths.
	if !validDescriptor(d, media) {
		return fmt.Errorf("invalid collector blob descriptor")
	}
	// Repeated references share one descriptor after checking compatible metadata.
	if existing, ok := i.blobs[d.Digest]; ok {
		// Conflicting declarations are malformed even if the content hash matches.
		if existing.size != d.Size || existing.mediaType != media {
			return fmt.Errorf("conflicting collector blob descriptor")
		}
		return nil
	}
	path := filepath.Join(directory, "blobs", "sha256", d.Digest[7:])
	f, err := openRegular(path)
	// Refuse symlinks and non-regular filesystem objects.
	if err != nil {
		return err
	}
	info, err := f.Stat()
	// Files must match the size authorized by the release index.
	if err != nil || info.Size() != d.Size {
		f.Close()
		return fmt.Errorf("collector blob size mismatch")
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	// A corrupted blob invalidates the entire image before serving begins.
	if err != nil || "sha256:"+hex.EncodeToString(h.Sum(nil)) != d.Digest {
		f.Close()
		return fmt.Errorf("collector blob digest mismatch")
	}
	i.blobs[d.Digest] = blob{file: f, size: d.Size, mediaType: media}
	return nil
}

// openRegular rejects symlinked files and directory components under the layout.
func openRegular(path string) (*os.File, error) {
	// Check the file plus its layout-internal parents, excluding the caller's trusted root.
	for candidate, depth := path, 0; depth < 3; candidate, depth = filepath.Dir(candidate), depth+1 {
		info, err := os.Lstat(candidate)
		// No layout component may redirect reads outside the release directory.
		if err != nil {
			return nil, err
		}
		// A symlink is rejected even when its current target would be a valid file.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink in collector image layout")
		}
	}
	f, err := os.Open(path)
	// Open errors preserve the original filesystem cause.
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	// Pipes/devices/directories are never valid image objects.
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("collector image object is not regular")
	}
	return f, nil
}

// readRegular bounds JSON metadata reads before parsing.
func readRegular(path string, limit int64) ([]byte, error) {
	f, err := openRegular(path)
	// Preserve object validation failures.
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	// A bounded parser must reject rather than silently truncate oversized input.
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("collector image metadata exceeds limit")
	}
	return raw, err
}

// digestBytes returns the registry and OCI spelling of a SHA-256 digest.
func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Files returns only referenced layout files, with the atomic index publication last.
func (i *Image) Files() []string {
	files := make([]string, 0, len(i.blobs)+2)
	// Ignore unreferenced directory entries rather than copying arbitrary files.
	for digest := range i.blobs {
		files = append(files, filepath.Join("blobs", "sha256", digest[7:]))
	}
	sort.Strings(files)
	return append(files, "oci-layout", "index.json")
}

// validDescriptor enforces both path-safe content IDs and per-object size/media bounds.
func validDescriptor(d descriptor, media string) bool {
	return digestPattern.MatchString(d.Digest) && d.Size > 0 && d.Size <= maxBlob && d.MediaType == media
}
