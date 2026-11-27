package model

import "regexp"

// DefaultSwarm preserves the implicit membership of all pre-swarm applications.
const DefaultSwarm = "default"

var swarmNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ValidSwarmName keeps CLI selectors and API paths unambiguous.
func ValidSwarmName(name string) bool { return swarmNamePattern.MatchString(name) }

// Swarm is a logical group; its immutable ID survives membership changes.
type Swarm struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SwarmName applies the legacy default without rewriting retained specifications.
func SwarmName(name string) string {
	if name == "" {
		return DefaultSwarm
	}
	return name
}
