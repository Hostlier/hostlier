package config

import (
	"encoding/json"
	"github.com/google/go-containerregistry/pkg/name"
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
	// Ambiguous inputs must fail before credential lookup or local build execution.
	if (a.Context == "") == (a.Archive == "") {
		return invalid("Container requires exactly one of context or archive")
	}
	// Custom Dockerfiles share one portable containment check across init, validation and deploy.
	if !validContainerDockerfile(a) {
		return invalid("Dockerfile must be relative to its container context")
	}
	return validateContainerArchitecture(a.Architecture)
}

// ValidateRegistryImage checks an inert registry request independently of local container source syntax.
func ValidateRegistryImage(image, architecture string) error {
	// An empty reference cannot select the registry library's implicit defaults.
	if image == "" {
		return invalid("Image must be a valid registry image reference")
	}
	// Registry coordinates never contain URL userinfo or a transport scheme.
	if _, err := name.ParseReference(image); err != nil {
		return invalid("Image must be a valid registry image reference")
	}
	return validateContainerArchitecture(architecture)
}

// validateContainerArchitecture defers omitted targets to the receiving controller, never the laptop CPU.
func validateContainerArchitecture(architecture string) error {
	// Explicit constraints must name a supported Linux image architecture.
	if architecture != "" && architecture != "amd64" && architecture != "arm64" {
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
