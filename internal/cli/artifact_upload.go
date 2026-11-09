package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"hostlier/internal/artifact"
	"hostlier/internal/model"
)

// transferArtifact negotiates durable progress before uploading or reusing server content.
func (s *session) transferArtifact(result artifact.Result) (artifact.Result, error) {
	status, err := s.artifactUploadStatus(result)
	// Authentication and integrity errors cannot downgrade into an unverified upload.
	if err != nil {
		return result, err
	}
	// The server independently verified the exact archive, image and configuration tuple.
	if status.Committed {
		fmt.Fprintln(s.errOut, "Reusing verified image already on the server")
		return result, nil
	}
	// Resume only after the local prefix matches the retained server bytes.
	if err = s.uploadArtifact(result, status); err != nil {
		return result, err
	}
	var committed artifact.Result
	body := map[string]any{"image": result.Image, "configDigest": result.ConfigDigest, "archiveDigest": result.ArchiveDigest, "size": result.Size}
	// Commit itself is retryable by verified content identity, not a new deployment operation.
	err = s.request("POST", "/v1/artifacts/commit", body, &committed)
	return committed, err
}

// artifactUploadStatus negotiates chunk size and falls back only for an absent endpoint.
func (s *session) artifactUploadStatus(result artifact.Result) (artifact.UploadStatus, error) {
	var status artifact.UploadStatus
	err := s.request("POST", "/v1/artifacts/status", result, &status)
	var failure *model.Error
	// Older controllers retain their original bounded protocol without resume discovery.
	if errors.As(err, &failure) && failure.Code == "NOT_FOUND" {
		return artifact.UploadStatus{ChunkBytes: artifact.LegacyChunkBytes}, nil
	}
	// A revoked credential or invalid receipt must remain an error.
	if err != nil {
		return status, err
	}
	// Bound allocations and reject impossible progress before reading local files.
	if status.ChunkBytes <= 0 || status.ChunkBytes > artifact.ChunkBytes || status.Offset < 0 || status.Offset > result.Size {
		return status, model.Err("ARTIFACT_OFFSET", "Invalid upload status from controller")
	}
	// A committed receipt must cover the complete archive.
	if status.Committed && status.Offset != result.Size {
		return status, model.Err("ARTIFACT_OFFSET", "Incomplete committed artifact status")
	}
	return status, nil
}

// uploadArtifact sends negotiated chunks and never assumes a retained prefix is correct.
func (s *session) uploadArtifact(result artifact.Result, status artifact.UploadStatus) error {
	f, err := os.Open(result.Path)
	// Missing cached output cannot produce a successful upload receipt.
	if err != nil {
		return err
	}
	defer f.Close()
	// Verify and consume the prefix before continuing at its exact byte boundary.
	if err = verifyUploadPrefix(f, status); err != nil {
		return err
	}
	// Make resumed work visible without printing archive contents or credentials.
	if status.Offset > 0 {
		fmt.Fprintf(s.errOut, "Resuming artifact upload at %d of %d bytes\n", status.Offset, result.Size)
	}
	buffer := make([]byte, status.ChunkBytes)
	// Each iteration appends only the remaining prepared bytes.
	for offset := status.Offset; offset < result.Size; {
		count := min(int64(len(buffer)), result.Size-offset)
		n, err := io.ReadFull(f, buffer[:count])
		// Changed or truncated local files cannot be committed as the original image.
		if err != nil {
			return model.Err("ARTIFACT_READ", "Prepared archive changed during upload")
		}
		// Exact byte replays remain safe after a lost acknowledgement.
		if err = s.sendArtifactChunk(result, offset, buffer[:n]); err != nil {
			return err
		}
		offset += int64(n)
	}
	return nil
}

// verifyUploadPrefix checks the server's checksum and positions the local reader for append.
func verifyUploadPrefix(r io.Reader, status artifact.UploadStatus) error {
	// New uploads and legacy controllers have no prefix to verify.
	if status.Offset == 0 {
		return nil
	}
	digest, err := artifact.PrefixDigest(r, status.Offset)
	// A changed prefix requires operator inspection instead of silently joining unrelated bytes.
	if err != nil || digest != status.PrefixDigest {
		return model.Err("ARTIFACT_DIGEST", "Retained upload prefix does not match the prepared archive")
	}
	return nil
}

// sendArtifactChunk checks both the lower and upper bounds of a durable acknowledgement.
func (s *session) sendArtifactChunk(result artifact.Result, offset int64, data []byte) error {
	body := map[string]any{"offset": offset, "data": data, "requestId": fmt.Sprintf("%s-%d", result.ArchiveDigest, offset)}
	var response struct {
		Offset int64 `json:"offset"`
	}
	// Preserve deterministic byte replay through the existing authenticated request path.
	if err := s.request("POST", "/v1/artifacts/"+result.ArchiveDigest+"/chunks", body, &response); err != nil {
		return err
	}
	// Concurrent exact replays may report a longer prefix, but never beyond this artifact.
	if response.Offset < offset+int64(len(data)) || response.Offset > result.Size {
		return model.Err("ARTIFACT_OFFSET", "Server did not retain the expected artifact bytes")
	}
	return nil
}
