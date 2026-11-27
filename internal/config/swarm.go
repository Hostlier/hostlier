package config

import "hostlier/internal/model"

// ValidateSwarm validates grouping metadata without altering runtime identity or legacy fingerprints.
func ValidateSwarm(p model.Project) error {
	if p.Swarm != "" && !model.ValidSwarmName(p.Swarm) {
		return invalid("Swarm must be a lowercase name with letters, digits and hyphens")
	}
	for _, sidecar := range p.Sidecars {
		if sidecar.Swarm != "" {
			return invalid("Sidecars inherit the application's swarm")
		}
	}
	if p.Kind == "memory" && p.Swarm != "" && p.Swarm != model.DefaultSwarm {
		return invalid("Named swarms currently require a K3s application")
	}
	return nil
}
