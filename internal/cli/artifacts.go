package cli

import (
	"hostlier/internal/model"
)

// prepareArtifacts packages and uploads compiled inputs before requesting a deployment plan.
func (s *session) prepareArtifacts(p model.Project) (model.Project, error) {
	var err error
	// Container builds and prebuilt archives share the verified upload path.
	p, err = s.prepareContainerSource(p)
	// Never continue with another source after container preparation fails.
	if err != nil {
		return p, err
	}
	// Sidecar artifacts follow the exact same pipeline as standalone agents.
	for _, name := range model.MemberNames(p) {
		member, err := s.prepareArtifacts(p.Sidecars[name])
		// Do not request approval for only part of the intended group.
		if err != nil {
			return p, err
		}
		p.Sidecars[name] = member
	}
	return p, nil
}

// validateLocalArtifacts inspects every native payload without building or contacting a controller.
func (s *session) validateLocalArtifacts(p model.Project) error {
	// Validation checks paths and archive platforms but never runs a Dockerfile.
	if p.Source != nil && p.Source.Container != nil {
		// Invalid local inputs must fail before a plan or upload is requested.
		if err := validateContainerInput(s.containerPaths(*p.Source.Container)); err != nil {
			return err
		}
	}
	// Members retain independent source and platform validation.
	for _, member := range p.Sidecars {
		// Report the first invalid component before attempting any deployment.
		if err := s.validateLocalArtifacts(member); err != nil {
			return err
		}
	}
	return nil
}
