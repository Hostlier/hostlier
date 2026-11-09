package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// TestContainerCustomDockerfile keeps monorepo build recipes relative to their complete workspace context.
func TestContainerCustomDockerfile(t *testing.T) {
	root := t.TempDir()
	recipes := filepath.Join(root, "recipes")
	// Build fixtures must exist before offline container validation.
	if err := os.Mkdir(recipes, 0700); err != nil {
		t.Fatal(err)
	}
	// No actual image build is needed for path validation.
	if err := os.WriteFile(filepath.Join(recipes, "web.Dockerfile"), []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := model.ContainerSource{Context: root, Dockerfile: "recipes/web.Dockerfile", Architecture: "amd64"}
	// A contained recipe is a valid offline input.
	if err := validateContainerInput(source); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "/tmp/outside", ".", "recipes\\outside"} { // Reject all portable forms of an escaping or non-file recipe.
		source.Dockerfile = path
		// Invalid recipes must fail before Docker runs.
		if err := validateContainerInput(source); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}

// TestContainerRecipeSymlinkEscape rejects a valid-looking relative filename that resolves outside the context.
func TestContainerRecipeSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "Dockerfile")
	// The target exists so failure must come from containment, not a dangling symlink.
	if err := os.WriteFile(outside, []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The caller must not substitute an external recipe through a link.
	if err := os.Symlink(outside, filepath.Join(root, "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	// Filesystem resolution enforces the lexical source boundary.
	if err := validateContainerInput(model.ContainerSource{Context: root, Architecture: "amd64"}); err == nil {
		t.Fatal("external recipe accepted")
	}
}

// TestContainerBuildForwardsCustomRecipe passes literal workspace paths to Docker without a shell.
func TestContainerBuildForwardsCustomRecipe(t *testing.T) {
	directory := t.TempDir()
	capture := filepath.Join(directory, "args")
	// A fake executable records arguments and cannot build or publish an image.
	if err := os.WriteFile(filepath.Join(directory, "docker"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_FILE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("CAPTURE_FILE", capture)
	s := session{ctx: context.Background(), errOut: io.Discard}
	source := model.ContainerSource{Context: filepath.Join(directory, "repo with spaces"), Dockerfile: "recipes/web.Dockerfile", Architecture: "arm64", Archive: filepath.Join(directory, "result.tar")}
	// Successful delegation must preserve every explicit source selection.
	if err := s.buildContainer(source); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	// Captured process arguments are the authority for this test.
	if err != nil {
		t.Fatal(err)
	}
	expected := "--file\n" + filepath.Join(source.Context, source.Dockerfile) + "\n" + source.Context + "\n"
	// Custom recipes must not alter architecture or context ownership.
	if !strings.HasSuffix(string(data), expected) || !strings.Contains(string(data), "linux/arm64\n") {
		t.Fatal(string(data))
	}
}
