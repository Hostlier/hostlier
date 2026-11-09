package cli

import (
	"fmt"
	"hostlier/internal/artifact"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// prepareContainerSource lowers local container inputs to the existing immutable image contract.
func (s *session) prepareContainerSource(p model.Project) (model.Project, error) {
	// Other workload sources retain their own packaging flow.
	if p.Source == nil || p.Source.Container == nil {
		return p, nil
	}
	// Sidecars may omit parent identity, but source ambiguity must always fail before a build.
	if p.Source.Image != "" || p.Image != "" {
		return p, model.Err("INVALID_CONFIG", "Select only one workload source")
	}
	receipt, err := s.prepareContainer(s.containerPaths(*p.Source.Container))
	// Existing authorization, resumability and integrity checks govern container uploads too.
	if err != nil {
		return p, err
	}
	p.Image = receipt.Image
	p.Source = &model.Source{Image: receipt.Image}
	return p, nil
}

// prepareContainer keeps the private build workspace alive until upload or failure completes.
func (s *session) prepareContainer(a model.ContainerSource) (artifact.Result, error) {
	// All local path and platform checks precede local code execution.
	if err := validateContainerInput(a); err != nil {
		return artifact.Result{}, err
	}
	dir, err := os.MkdirTemp("", "hostlier-container-")
	// A private temporary workspace prevents collisions with other CLI invocations.
	if err != nil {
		return artifact.Result{}, err
	}
	defer os.RemoveAll(dir)
	// A context explicitly authorizes a local Docker build; archives never execute application code.
	if a.Context != "" {
		a.Archive = filepath.Join(dir, "docker.tar")
		// Preserve build errors without uploading an old image as a fallback.
		if err = s.buildContainer(a); err != nil {
			return artifact.Result{}, err
		}
	}
	fmt.Fprintln(s.errOut, "Preparing verified container image for upload")
	receipt, err := artifact.PrepareContainer(dir, a.Archive, a.Architecture)
	// Only verified Linux image content may cross the controller boundary.
	if err != nil {
		return artifact.Result{}, err
	}
	return s.transferArtifact(receipt)
}

// containerPaths resolves all local inputs relative to the selected deployment file.
func (s *session) containerPaths(a model.ContainerSource) model.ContainerSource {
	root := filepath.Dir(s.projectPath())
	// Empty or absolute paths preserve their meaning instead of becoming an implicit context.
	if a.Context != "" && !filepath.IsAbs(a.Context) {
		a.Context = filepath.Join(root, a.Context)
	}
	// Archives follow the same project-relative rule as native compiled artifacts.
	if a.Archive != "" && !filepath.IsAbs(a.Archive) {
		a.Archive = filepath.Join(root, a.Archive)
	}
	return a
}

// validateContainerInput is safe for offline validate and does not require Docker.
func validateContainerInput(a model.ContainerSource) error {
	// The same source-mode rules apply to primary and sidecar containers.
	if err := config.ValidateContainerSource(a); err != nil {
		return err
	}
	// Prebuilt archives are inspected without invoking the Docker daemon.
	if a.Archive != "" {
		_, err := artifact.ContainerImage(a.Archive, a.Architecture)
		return err
	}
	info, err := os.Stat(a.Context)
	// An explicit build context must be an existing directory.
	if err != nil || !info.IsDir() {
		return model.Err("CONTAINER_CONTEXT", "Container context must be an existing directory")
	}
	info, err = os.Stat(filepath.Join(a.Context, defaultValue(a.Dockerfile, "Dockerfile")))
	// Require the standard Dockerfile rather than guessing a build system or running project hooks.
	if err != nil || !info.Mode().IsRegular() {
		return model.Err("CONTAINER_CONTEXT", "Container context requires a Dockerfile")
	}
	return validateContainerRecipePath(a)
}

// validateContainerRecipePath prevents a recipe symlink from escaping its selected build context.
func validateContainerRecipePath(a model.ContainerSource) error {
	root, err := filepath.EvalSymlinks(a.Context)
	// A broken context cannot authorize a different recipe root.
	if err != nil {
		return err
	}
	recipe, err := filepath.EvalSymlinks(filepath.Join(a.Context, defaultValue(a.Dockerfile, "Dockerfile")))
	// A dangling recipe remains an invalid build input.
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, recipe)
	// Only regular files below the explicit context can become executable build instructions.
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return model.Err("CONTAINER_CONTEXT", "Dockerfile must stay inside its container context")
	}
	return nil
}

// buildContainer runs the explicitly selected local Dockerfile with a bounded platform and output path.
func (s *session) buildContainer(a model.ContainerSource) error {
	fmt.Fprintf(s.errOut, "Building container locally for linux/%s\n", a.Architecture)
	args := []string{"buildx", "build", "--platform", "linux/" + a.Architecture, "--provenance=false", "--output", "type=docker,dest=" + a.Archive}
	// Monorepo apps keep their full workspace context while selecting their own reviewed recipe.
	if a.Dockerfile != "" {
		args = append(args, "--file", filepath.Join(a.Context, a.Dockerfile))
	}
	args = append(args, a.Context)
	command := exec.CommandContext(s.ctx, "docker", args...)
	command.Stdout, command.Stderr = s.errOut, s.errOut
	// Docker may be absent or a build may fail; neither permits a deployment with substituted content.
	if err := command.Run(); err != nil {
		return model.Err("CONTAINER_BUILD", "Local container build failed; verify Docker is running and inspect the build output")
	}
	return nil
}
