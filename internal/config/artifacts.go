package config

import (
	"encoding/json"
	"hostlier/internal/model"
	"path"
	"strings"
)

// ValidateInput checks a complete desired group before any local packaging or upload.
func ValidateInput(p model.Project) error {
	data, err := json.Marshal(p)
	// A copied shape prevents validation defaults from altering the authored artifact source.
	if err != nil {
		return err
	}
	var copy model.Project
	// Model JSON is produced locally and preserves every authored field.
	if err = json.Unmarshal(data, &copy); err != nil {
		return err
	}
	// Replace only locally prepared sources with a schema-valid placeholder for structural validation.
	if err = placeholderArtifacts(&copy); err != nil {
		return err
	}
	_, err = ValidateProject(copy)
	return err
}

// placeholderArtifacts makes artifact source syntax explicit without fetching registry content.
func placeholderArtifacts(p *model.Project) error {
	// Validate container source exclusivity before replacing its local paths.
	if err := placeholderContainer(p); err != nil {
		return err
	}
	// Sidecars are validated with the same parent bindings after all placeholders are applied.
	for name, member := range p.Sidecars {
		// A failed member must not be omitted from the group validation result.
		if err := placeholderArtifacts(&member); err != nil {
			return err
		}
		p.Sidecars[name] = member
	}
	return nil
}

// placeholderContainer validates local container syntax without running Docker.
func placeholderContainer(p *model.Project) error {
	// Registry images and native executables retain their existing validation paths.
	if p.Source == nil || p.Source.Container == nil {
		return nil
	}
	// Mixed sources and conflicting image identities cannot trigger local code execution.
	if p.Source.Image != "" || p.Image != "" {
		return invalid("Select only one workload source")
	}
	// Share source-mode validation with offline path checks in the CLI.
	if err := ValidateContainerSource(*p.Source.Container); err != nil {
		return err
	}
	p.Source = &model.Source{Image: "hostlier.local/prepared:validation"}
	return nil
}

// ValidateContainerSource checks portable input selection without requiring a complete parent workload.
func ValidateContainerSource(a model.ContainerSource) error {
	// Exactly one explicit input prevents an accidental build when an archive was intended.
	if (a.Context == "") == (a.Archive == "") {
		return invalid("Container requires exactly one of context or archive")
	}
	// Custom Dockerfiles share one portable containment check across init, validation and deploy.
	if !validContainerDockerfile(a) {
		return invalid("Dockerfile must be relative to its container context")
	}
	// Container architecture is never inferred from the laptop's CPU.
	if a.Architecture != "amd64" && a.Architecture != "arm64" {
		return invalid("Container architecture must be amd64 or arm64")
	}
	return nil
}

// validContainerDockerfile confines a selected recipe to the explicit container context.
func validContainerDockerfile(a model.ContainerSource) bool {
	// The standard Dockerfile remains the default for existing configurations.
	if a.Dockerfile == "" {
		return true
	}
	clean := path.Clean(a.Dockerfile)
	return a.Context != "" && clean != "." && !path.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.ContainsAny(a.Dockerfile, "\\\x00")
}
