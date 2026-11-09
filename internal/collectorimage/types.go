// Package collectorimage builds and serves the private, immutable collector image.
package collectorimage

import (
	"net/http"
	"os"
)

const Repository = "hostlier/collector"
const indexMediaType = "application/vnd.oci.image.index.v1+json"
const manifestMediaType = "application/vnd.oci.image.manifest.v1+json"
const configMediaType = "application/vnd.oci.image.config.v1+json"
const layerMediaType = "application/vnd.oci.image.layer.v1.tar+gzip"

// Platform identifies a supported Linux node architecture.
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

// PlatformInput supplies trusted release files for one image platform.
type PlatformInput struct{ Architecture, BinaryPath, CAPath string }

// descriptor identifies immutable OCI content.
type descriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	Platform  *Platform `json:"platform,omitempty"`
}

// imageIndex describes the exact platform manifests offered by this release.
type imageIndex struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []descriptor `json:"manifests"`
}

// manifest identifies the scratch image configuration and its single filesystem layer.
type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

// blob retains an open immutable file, avoiding path traversal and replacement races.
type blob struct {
	file      *os.File
	size      int64
	mediaType string
}

// Image is a validated OCI layout served only through fixed registry read endpoints.
type Image struct {
	digest    string
	index     []byte
	blobs     map[string]blob
	manifests map[string]bool
	platforms []Platform
}

// Digest returns the immutable multi-platform index digest used for pulls.
func (i *Image) Digest() string { return i.digest }

// Reference returns a Docker-compatible digest reference; hostname may include a port.
func (i *Image) Reference(hostname string) string {
	return hostname + "/" + Repository + "@" + i.digest
}

// Platforms returns the architectures available to newly joined nodes.
func (i *Image) Platforms() []Platform { return append([]Platform(nil), i.platforms...) }

// Close releases file handles retained for concurrency-safe registry reads.
func (i *Image) Close() error {
	var first error
	// Attempt every close even when an earlier descriptor fails.
	for _, b := range i.blobs {
		// Preserve the first close failure for the owner.
		if err := b.file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

var _ http.Handler = (*Image)(nil)
