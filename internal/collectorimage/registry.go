package collectorimage

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// ServeHTTP exposes only immutable registry reads; authentication belongs to the controller.
func (i *Image) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	// Registry uploads, deletions and catalog enumeration are never supported.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		registryError(w, r, 405, "UNSUPPORTED")
		return
	}
	// The standard version probe is necessary for Docker and containerd clients.
	if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
		serveObject(w, r, bytes.NewReader([]byte("{}")), "application/json", "")
		return
	}
	prefix := "/v2/" + Repository + "/"
	// Only this repository is exposed; there are no catalog/tag-list endpoints.
	if !strings.HasPrefix(r.URL.Path, prefix) {
		registryError(w, r, 404, "NAME_UNKNOWN")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	// Digest references are the only accepted image identifiers.
	if len(parts) != 2 || !digestPattern.MatchString(parts[1]) {
		registryError(w, r, 404, "NAME_UNKNOWN")
		return
	}
	i.serveDigest(w, r, parts[0], parts[1])
}

// serveDigest keeps manifest and blob namespaces distinct despite sharing storage.
func (i *Image) serveDigest(w http.ResponseWriter, r *http.Request, kind, digest string) {
	// A multi-platform pull begins at the digest of the immutable root index.
	if kind == "manifests" && digest == i.digest {
		serveObject(w, r, bytes.NewReader(i.index), indexMediaType, digest)
		return
	}
	b, ok := i.blobs[digest]
	// Unknown objects are not probed on disk and cannot disclose other controller files.
	if !ok {
		registryError(w, r, 404, "BLOB_UNKNOWN")
		return
	}
	// Platform manifests are reachable only from the manifest endpoint.
	if kind == "manifests" && i.manifests[digest] {
		serveObject(w, r, io.NewSectionReader(b.file, 0, b.size), b.mediaType, digest)
		return
	}
	// Configurations and layers can be downloaded as standard registry blobs.
	if kind == "blobs" && !i.manifests[digest] {
		serveObject(w, r, io.NewSectionReader(b.file, 0, b.size), b.mediaType, digest)
		return
	}
	registryError(w, r, 404, "BLOB_UNKNOWN")
}

// serveObject supports HEAD and byte ranges without mutable shared seek positions.
func serveObject(w http.ResponseWriter, r *http.Request, body io.ReadSeeker, media, digest string) {
	w.Header().Set("Content-Type", media)
	w.Header().Set("Cache-Control", "private, max-age=3600, immutable")
	// Digest headers make image bytes verifiable independently of TLS delivery.
	if digest != "" {
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("ETag", `"`+digest+`"`)
	}
	http.ServeContent(w, r, "", time.Time{}, body)
}

// registryError returns standard registry errors without filesystem or credential detail.
func registryError(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// HEAD responses carry only headers, including for missing resources.
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": "collector image request unavailable"}}})
}
