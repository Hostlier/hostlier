package config

import "hostlier/internal/model"

// validateProjectWorkflow keeps package bindings separate from literal, secret and built-in resource inputs.
func validateProjectWorkflow(p model.Project) (model.Project, error) {
	// Existing projects need no workflow declaration.
	if p.Workflow == nil {
		return p, nil
	}
	// Reject graph errors before any controller or package interaction.
	if err := p.Workflow.Validate(); err != nil {
		return p, invalid(err.Error())
	}
	// One environment name must have exactly one producer.
	for env := range p.Workflow.Bindings {
		_, literal := p.Env[env]
		_, secret := p.Secrets[env]
		_, resource := p.Bindings[env]
		// Preserve deterministic binding precedence by rejecting collisions.
		if literal || secret || resource {
			return p, invalid("Workflow binding conflicts with another environment value")
		}
	}
	return p, nil
}
