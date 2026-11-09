package runtime

import (
	"context"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"os/exec"
)

// RemoveArtifact removes only expired Hostlier image metadata from the same containerd namespace used for import.
func (k *K3s) RemoveArtifact(ctx context.Context, image string) error {
	k.base.mu.Lock()
	defer k.base.mu.Unlock()
	// Controller references are checked before selecting this exact immutable cache identity.
	if !artifact.IsImage(image) || !digestPattern.MatchString(image) {
		return model.Err("ARTIFACT_ID", "Invalid cleanup image")
	}
	command := exec.CommandContext(ctx, k.options.KubeBinary, "ctr", "-n", "k8s.io", "images", "rm", artifact.RuntimeImage(image))
	// Failed containerd access retains the controller's cleanup marker for retry.
	if err := command.Run(); err != nil {
		return model.Err("ARTIFACT_REMOVE", "Artifact image cleanup pending")
	}
	return nil
}
