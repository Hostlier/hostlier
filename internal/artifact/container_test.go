package artifact

import (
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"os"
	"path/filepath"
	"testing"
)

// containerFixture preserves real runtime configuration through portable archive preparation.
func containerFixture(t *testing.T) string {
	t.Helper()
	image, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: "amd64", Config: v1.Config{User: "node", Cmd: []string{"node", "server.mjs"}}})
	// Invalid fixture configuration must not weaken platform verification.
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "container.tar")
	tag, _ := name.NewTag("local/test:container")
	// Serialize the same Docker transport users obtain from docker save.
	if err = tarball.WriteToFile(path, tag, image); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestContainerArchiveRoundTrip verifies portable publication without changing the app launcher.
func TestContainerArchiveRoundTrip(t *testing.T) {
	path := containerFixture(t)
	receipt, err := PrepareContainer(t.TempDir(), path, "amd64")
	// Normalization must produce an independently verifiable upload receipt.
	if err != nil {
		t.Fatal(err)
	}
	image, err := ContainerImage(receipt.Path, "amd64")
	// Reopening catches archive/tag/manifest disagreement before controller upload.
	if err != nil {
		t.Fatal(err)
	}
	config, _ := image.ConfigFile()
	// Generic containers must not receive Harnest entrypoint or profile overrides.
	if config.Config.User != "node" || len(config.Config.Cmd) != 2 || config.Config.Cmd[1] != "server.mjs" {
		t.Fatal(config.Config)
	}
	data, err := os.ReadFile(receipt.Path)
	// Use the actual controller store to exercise hash and manifest verification.
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Root: t.TempDir(), UploadOwner: "paired-cli"}
	// A normalized tiny fixture fits one bounded upload chunk.
	if _, err = store.Chunk(receipt.ArchiveDigest, 0, data); err != nil {
		t.Fatal(err)
	}
	// A controller must accept the image without registry publication.
	if _, err = store.Commit(receipt); err != nil {
		t.Fatal(err)
	}
	// Subsequent runtime preparation uses the retained immutable image.
	if _, err = store.Inspect(receipt.Image); err != nil {
		t.Fatal(err)
	}
}

// TestContainerArchiveRejectsUnsafeInputs prevents invalid platforms and nonfiles from uploading.
func TestContainerArchiveRejectsUnsafeInputs(t *testing.T) {
	path := containerFixture(t)
	// Wrong architectures are errors rather than implicit emulation.
	if _, err := ContainerImage(path, "arm64"); err == nil {
		t.Fatal("accepted wrong platform")
	}
	// Unknown platform names never reach an image parser.
	if _, err := ContainerImage(path, "darwin"); err == nil {
		t.Fatal("accepted invalid platform")
	}
	// Directories and missing files cannot be mistaken for image archives.
	if _, err := ContainerImage(t.TempDir(), "amd64"); err == nil {
		t.Fatal("accepted directory")
	}
	large := filepath.Join(t.TempDir(), "large.tar")
	file, err := os.Create(large)
	// A sparse file beyond the former 512 MiB cap consumes no real disk space.
	if err != nil {
		t.Fatal(err)
	}
	err = file.Truncate((512 << 20) + 1)
	file.Close()
	// Successful sparse creation is required for this size check.
	if err != nil {
		t.Fatal(err)
	}
	// Size alone cannot reject a valid portable archive.
	if err = validateContainerArchive(large); err != nil {
		t.Fatal("rejected large regular archive", err)
	}
	// Preparation must hash the complete archive instead of stopping at the old cap.
	digest, size, err := FileDigest(large)
	if err != nil || size != (512<<20)+1 || len(digest) != 64 {
		t.Fatal("large archive digest was truncated", size, digest, err)
	}
}
