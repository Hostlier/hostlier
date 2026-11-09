// Package artifact packages precompiled workloads without executing their code.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// Result identifies the immutable archive and its runnable image configuration.
type Result struct {
	Image         string `json:"image"`
	ConfigDigest  string `json:"configDigest"`
	ArchiveDigest string `json:"archiveDigest"`
	Size          int64  `json:"size"`
	Path          string `json:"-"`
}

// writeImage retains both image content and its transport checksum for verified upload.
func writeImage(dir string, image v1.Image) (Result, error) {
	image, err := canonicalArchive(dir, image)
	// Docker transport has a canonical schema-2 manifest distinct from some OCI base manifests.
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(filepath.Join(dir, "canonical.tar"))
	digest, err := image.Digest()
	// Image identity must exist before choosing its local runtime name.
	if err != nil {
		return Result{}, err
	}
	config, err := image.ConfigName()
	// Docker verifies the runnable configuration identity after import.
	if err != nil {
		return Result{}, err
	}
	tag, err := name.NewTag("hostlier.local/artifacts:" + digest.Hex)
	// Tags are generated solely from validated digest bytes.
	if err != nil {
		return Result{}, err
	}
	path := filepath.Join(dir, "image.tar")
	// The same portable archive is imported by Docker and k3s containerd.
	if err = tarball.WriteToFile(path, tag, image); err != nil {
		return Result{}, err
	}
	checksum, size, err := FileDigest(path)
	return Result{Image: "hostlier.local/artifacts@" + digest.String(), ConfigDigest: config.String(), ArchiveDigest: checksum, Size: size, Path: path}, err
}

// FileDigest hashes an archive as a stream without imposing an image-size ceiling.
func FileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	// Missing output cannot become a valid transport receipt.
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), size, err
}

// canonicalArchive derives the manifest that both importers reconstruct from the portable transport.
func canonicalArchive(dir string, image v1.Image) (v1.Image, error) {
	tag, err := name.NewTag("hostlier.local/staging:prepared")
	// A fixed temporary tag has no deployment identity or mutable registry lookup.
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "canonical.tar")
	// Normalize OCI descriptor media types through the exact portable archive reader used by commit.
	if err = tarball.WriteToFile(path, tag, image); err != nil {
		return nil, err
	}
	return tarball.ImageFromPath(path, nil)
}
