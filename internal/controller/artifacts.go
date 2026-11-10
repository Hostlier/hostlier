package controller

import (
	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"net/http"
	"path/filepath"
	"runtime"
)

// artifactChunkRequest carries one bounded resumable upload segment.
type artifactChunkRequest struct {
	Offset    int64  `json:"offset"`
	Data      []byte `json:"data"`
	RequestID string `json:"requestId"`
}

// apiArtifacts accepts authenticated staging without starting or publishing workloads.
func (c *Controller) apiArtifacts(r *http.Request, p []string, actor string) (any, error) {
	store := artifact.Store{Root: filepath.Join(c.opts.StateDir, "artifacts")}
	// Container platforms describe the Linux guest target even for a development controller on macOS.
	if matches(r, p, "GET", 3, "platform") {
		return map[string]string{"os": "linux", "arch": runtime.GOARCH}, nil
	}
	// Scoped clients cannot append to another client's staging or committed archives.
	if c.scopedClient(actor) {
		store.UploadOwner = actor
	}
	// Server-first registry preparation is inert and uses only the server's credential source.
	if matches(r, p, "POST", 3, "pull") {
		return c.pullRegistryArtifact(r, store, actor)
	}
	// Negotiation returns metadata only, scoped to the authenticated uploader.
	if matches(r, p, "POST", 3, "status") {
		var receipt artifact.Result
		// Status must bind every claimed image and archive identity.
		if err := decode(r, &receipt); err != nil {
			return nil, err
		}
		return store.Status(receipt)
	}
	// Chunk requests remain within the ordinary API request-body limit.
	if matches(r, p, "POST", 4, "chunks") {
		var chunk artifactChunkRequest
		// Strict decoding rejects unexpected upload fields.
		if err := decode(r, &chunk); err != nil {
			return nil, err
		}
		offset, err := store.Chunk(p[2], chunk.Offset, chunk.Data)
		return map[string]int64{"offset": offset}, err
	}
	// Commit verifies all content hashes before returning an image reference.
	if matches(r, p, "POST", 3, "commit") {
		var receipt artifact.Result
		// No image becomes available from unvalidated transport metadata.
		if err := decode(r, &receipt); err != nil {
			return nil, err
		}
		return store.Commit(receipt)
	}
	return nil, model.Err("NOT_FOUND", "Artifact endpoint not found")
}
