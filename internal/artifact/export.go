package artifact

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hostlier/internal/model"
	"io"
	"strings"
)

// VerifyExport verifies runnable config identity after daemons normalize transport manifests.
func VerifyExport(reader io.Reader, receipt Result) error {
	archive := tar.NewReader(reader)
	configs := map[string]bool{}
	var manifests []exportManifest
	// The export is streamed rather than retained or extracted onto the host filesystem.
	for {
		header, err := archive.Next()
		// A complete archive is verified against its selected manifest below.
		if err == io.EOF {
			break
		}
		// Truncated daemon exports cannot confirm the imported image.
		if err != nil {
			return err
		}
		// Only small JSON metadata needs allocation; layer bytes are streamed past by tar.Reader.
		if header.Name == "manifest.json" {
			// A local single-image export cannot legitimately require an unbounded manifest.
			if err = json.NewDecoder(io.LimitReader(archive, 1024*1024)).Decode(&manifests); err != nil {
				return err
			}
		} else if expectedConfigPath(header.Name, receipt.ConfigDigest) { // Docker and OCI exporters use two canonical config filenames.
			sum := sha256.New()
			_, err = io.Copy(sum, io.LimitReader(archive, 4*1024*1024))
			configs[header.Name] = err == nil && "sha256:"+hex.EncodeToString(sum.Sum(nil)) == receipt.ConfigDigest
		}
	}
	return verifyExportManifest(manifests, configs, RuntimeImage(receipt.Image))
}

// exportManifest selects the runnable config associated with the imported local content tag.
type exportManifest struct {
	Config   string
	RepoTags []string
}

// expectedConfigPath recognizes only content-addressed filenames for the reviewed config.
func expectedConfigPath(path, digest string) bool {
	hex := strings.TrimPrefix(digest, "sha256:")
	return path == hex+".json" || path == "blobs/sha256/"+hex
}

// verifyExportManifest ties the verified configuration bytes to the actual requested runtime tag.
func verifyExportManifest(manifests []exportManifest, configs map[string]bool, tag string) error {
	// Every exported manifest identifies its own configuration and local reference set.
	for _, manifest := range manifests {
		// A config not present or altered in the export cannot confirm identity.
		if !configs[manifest.Config] {
			continue
		}
		// Only the exact selected local tag can authorize execution.
		for _, actual := range manifest.RepoTags {
			// Exact equality prevents an unrelated image in the daemon from satisfying verification.
			if actual == tag {
				return nil
			}
		}
	}
	return model.Err("ARTIFACT_IMPORT", "Imported image configuration does not match approved artifact")
}
