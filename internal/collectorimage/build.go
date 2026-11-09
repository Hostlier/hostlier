package collectorimage

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Build creates a new deterministic OCI layout from trusted release executables and CA bundles.
func Build(directory string, inputs []PlatformInput) (err error) {
	// Do not overwrite a previously reviewed release image.
	if err = os.Mkdir(directory, 0755); err != nil {
		return err
	}
	// Clean up only this build's newly created output on failure.
	defer func() {
		// Remove only the fresh output directory owned by this failed build.
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	// Content-addressed objects share one fixed, traversal-free storage location.
	if err = os.MkdirAll(filepath.Join(directory, "blobs", "sha256"), 0755); err != nil {
		return err
	}
	index, err := buildPlatforms(directory, inputs)
	// A release index is published only after every platform succeeds.
	if err != nil {
		return err
	}
	raw, err := json.Marshal(index)
	// Metadata encoding errors leave no apparently complete release directory.
	if err != nil {
		return err
	}
	// The OCI layout marker enables standard tooling to recognize the directory.
	if err = os.WriteFile(filepath.Join(directory, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "index.json"), raw, 0644)
}

// buildPlatforms orders and deduplicates supported platforms for stable index hashes.
func buildPlatforms(directory string, inputs []PlatformInput) (imageIndex, error) {
	index := imageIndex{SchemaVersion: 2, MediaType: indexMediaType}
	// A release supports the two Linux architectures shipped by Hostlier.
	if len(inputs) == 0 || len(inputs) > 2 {
		return index, fmt.Errorf("provide one or two collector platforms")
	}
	inputs = append([]PlatformInput(nil), inputs...)
	// Stable architecture order makes index digests repeatable.
	sort.Slice(inputs, func(a, b int) bool { return inputs[a].Architecture < inputs[b].Architecture })
	seen := map[string]bool{}
	// Build each platform with independent configuration and filesystem digests.
	for _, input := range inputs {
		p := Platform{OS: "linux", Architecture: input.Architecture}
		// Reject invalid architecture declarations before reading executable paths.
		if !validPlatform(&p) || seen[p.Architecture] {
			return index, fmt.Errorf("invalid or duplicate collector architecture")
		}
		seen[p.Architecture] = true
		d, err := buildPlatform(directory, input)
		// Do not publish a partial architecture set.
		if err != nil {
			return index, err
		}
		d.Platform = &p
		index.Manifests = append(index.Manifests, d)
	}
	return index, nil
}

// buildPlatform emits one scratch filesystem plus its OCI runtime configuration.
func buildPlatform(directory string, input PlatformInput) (descriptor, error) {
	layer, diffID, err := buildLayer(directory, input)
	// A missing executable or trust bundle invalidates the platform.
	if err != nil {
		return descriptor{}, err
	}
	config := map[string]any{"architecture": input.Architecture, "os": "linux", "config": map[string]any{"User": "0:0", "Entrypoint": []string{"/hostlier-collector"}, "Cmd": []string{"--config=/etc/otelcol/collector.yaml"}}, "rootfs": map[string]any{"type": "layers", "diff_ids": []string{diffID}}}
	configDescriptor, err := writeJSONBlob(directory, config, configMediaType)
	// The manifest references only fully persisted configuration content.
	if err != nil {
		return descriptor{}, err
	}
	return writeJSONBlob(directory, manifest{SchemaVersion: 2, MediaType: manifestMediaType, Config: configDescriptor, Layers: []descriptor{layer}}, manifestMediaType)
}

// writeJSONBlob stores canonical Go-encoded JSON under its digest.
func writeJSONBlob(directory string, value any, media string) (descriptor, error) {
	raw, err := json.Marshal(value)
	// Metadata must encode successfully before calculating its content identity.
	if err != nil {
		return descriptor{}, err
	}
	d := descriptor{Digest: digestBytes(raw), Size: int64(len(raw)), MediaType: media}
	return d, os.WriteFile(filepath.Join(directory, "blobs", "sha256", d.Digest[7:]), raw, 0644)
}

// buildLayer streams deterministic gzip/tar bytes without loading executables into RAM.
func buildLayer(directory string, input PlatformInput) (descriptor, string, error) {
	path := filepath.Join(directory, "layer.tmp")
	output, err := os.Create(path)
	// A writable temporary layer is required before streaming input files.
	if err != nil {
		return descriptor{}, "", err
	}
	compressed := sha256.New()
	zipper, _ := gzip.NewWriterLevel(io.MultiWriter(output, compressed), gzip.BestCompression)
	zipper.Header.ModTime = time.Unix(0, 0)
	raw := sha256.New()
	archive := tar.NewWriter(io.MultiWriter(zipper, raw))
	err = writeLayerContents(archive, input)
	// Close in order so tar padding and gzip trailers are included in both digests.
	for _, close := range []func() error{archive.Close, zipper.Close, output.Close} {
		closeErr := close()
		// Preserve the first input or flush failure.
		if err == nil {
			err = closeErr
		}
	}
	// A partial filesystem never receives a public digest filename.
	if err != nil {
		return descriptor{}, "", err
	}
	info, err := os.Stat(path)
	// The finished compressed layer must fit the reader's explicit blob budget.
	if err != nil {
		return descriptor{}, "", err
	}
	d := descriptor{MediaType: layerMediaType, Digest: "sha256:" + hex.EncodeToString(compressed.Sum(nil)), Size: info.Size()}
	return d, "sha256:" + hex.EncodeToString(raw.Sum(nil)), os.Rename(path, filepath.Join(directory, "blobs", "sha256", d.Digest[7:]))
}

// writeLayerContents installs only the executable and public certificate trust bundle.
func writeLayerContents(archive *tar.Writer, input PlatformInput) error {
	// Explicit directory permissions avoid relying on extraction-tool defaults.
	for _, name := range []string{"etc/", "etc/ssl/", "etc/ssl/certs/"} {
		// Root-owned directories contain no machine-dependent timestamps or names.
		if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0755, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
			return err
		}
	}
	// The executable is the only application file in the scratch image.
	if err := appendFile(archive, input.BinaryPath, "hostlier-collector", 0755); err != nil {
		return err
	}
	return appendFile(archive, input.CAPath, "etc/ssl/certs/ca-certificates.crt", 0644)
}

// appendFile bounds and streams trusted source files with deterministic tar metadata.
func appendFile(archive *tar.Writer, path, name string, mode int64) error {
	f, err := openRegular(path)
	// Source paths must be regular files rather than symlinks or device nodes.
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	// Release inputs must be nonempty and fit the collector image's size budget.
	if err != nil || info.Size() <= 0 || info.Size() > maxBlob {
		return fmt.Errorf("invalid collector release file size")
	}
	// Tar headers intentionally omit host ownership names and filesystem timestamps.
	if err = archive.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: info.Size(), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
		return err
	}
	_, err = io.CopyN(archive, f, info.Size())
	return err
}
