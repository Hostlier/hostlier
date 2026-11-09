package packageexec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

const MaxBinaryBytes = 128 << 20
const ChunkBytes = 64 << 10

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Platform is the native executable target supplied by the receiving controller.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Source records public release coordinates; it never carries GitHub credentials.
type Source struct {
	Repository string `json:"repository,omitempty"`
	Version    string `json:"version,omitempty"`
	Asset      string `json:"asset,omitempty"`
}

// InstallRequest binds approval to the exact staged executable and its public source coordinates.
type InstallRequest struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Source Source `json:"source"`
}

// Validate rejects unsafe identities before staging or approving executable installation.
func (r InstallRequest) Validate() error {
	// Installation metadata is deliberately small and contains no arbitrary destination paths.
	if !ValidName(r.Name) || !ValidDigest(r.Digest) || r.Size <= 0 || r.Size > MaxBinaryBytes {
		return fmt.Errorf("invalid package installation identity")
	}
	// Public provenance must remain bounded in approval records.
	if len(r.Source.Repository) > 200 || len(r.Source.Version) > 200 || len(r.Source.Asset) > 250 {
		return fmt.Errorf("package source metadata exceeds limits")
	}
	return nil
}

// ValidName accepts only a single portable installation name.
func ValidName(name string) bool { return packageName.MatchString(name) }

// ValidDigest accepts canonical SHA-256 content identities.
func ValidDigest(value string) bool { return digestPattern.MatchString(value) }

// Digest measures the bytes that will be reviewed, transferred and installed.
func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// ReadSource snapshots an explicitly selected local executable before upload.
func ReadSource(path string) ([]byte, error) { return sourceBinary(path) }

// InstallVerified activates already staged bytes only when their approved identity still matches.
func (r Runner) InstallVerified(request InstallRequest, data []byte) (Installation, error) {
	// Validation is repeated at activation, independent of the earlier preview.
	if err := request.Validate(); err != nil {
		return Installation{}, err
	}
	// A staged artifact may not change after human review.
	if int64(len(data)) != request.Size || Digest(data) != request.Digest {
		return Installation{}, fmt.Errorf("package checksum or size changed")
	}
	// Preserve the same ancestry and target checks as local file installation.
	if err := prepareDirectory(r.Directory); err != nil {
		return Installation{}, err
	}
	// Refuse redirected or unsafe existing targets.
	if err := r.destination(request.Name); err != nil {
		return Installation{}, err
	}
	return r.installBytes(request.Name, data)
}
