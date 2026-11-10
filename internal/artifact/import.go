package artifact

import (
	"context"
	"io"
	"os"
)

// CheckAvailableSpace validates reserve capacity before controller-owned preparation writes.
func (s Store) CheckAvailableSpace(additional int64) error {
	// The controller owns this private directory; no client supplies its filesystem location.
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	return s.checkAvailableSpace(additional)
}

// Import publishes a locally downloaded archive through the same scoped hash verifier as CLI uploads.
func (s Store) Import(ctx context.Context, receipt Result) (Result, error) {
	status, err := s.Status(receipt)
	// An already committed archive still needs exact receipt verification.
	if err != nil {
		return Result{}, err
	}
	// Repeated controller preparations reuse verified immutable content.
	if status.Committed {
		return receipt, nil
	}
	file, err := os.Open(receipt.Path)
	// Only the server-produced archive can supply bytes for this receipt.
	if err != nil {
		return Result{}, err
	}
	defer file.Close()
	// Start from the beginning and use exact replay checks if a prior preparation stopped mid-import.
	buffer := make([]byte, ChunkBytes)
	// Each chunk reuses the upload store's exact replay, reserve and durability rules.
	for offset := int64(0); offset < receipt.Size; {
		if err := ctx.Err(); err != nil { // Shutdown and caller cancellation cannot continue writing staging content.
			return Result{}, err
		}
		n, err := io.ReadFull(file, buffer[:min(int64(len(buffer)), receipt.Size-offset)])
		if err != nil { // A changed local archive cannot become committed image content.
			return Result{}, err
		}
		if _, err := s.Chunk(receipt.ArchiveDigest, offset, buffer[:n]); err != nil { // Preserve reserve, replay and durability checks.
			return Result{}, err
		}
		offset += int64(n)
	}
	return s.Commit(receipt)
}
