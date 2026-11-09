package providers

import "hostlier/pkg/workflow"

// Capability exposes the provider's public project settings and bindable results.
func (p Package) Capability() workflow.Capability {
	modes := []string{"new-database", "existing"}
	fields := map[string]any{
		"engine":    map[string]any{"type": "string", "enum": []string{"postgres", "redis", "valkey"}},
		"mode":      map[string]any{"type": "string", "enum": modes},
		"database":  map[string]any{"type": "string", "description": "Database name for PostgreSQL new-database mode."},
		"keyPrefix": map[string]any{"type": "string", "description": "Key prefix for an existing Redis or Valkey service."},
	}
	// Only Aiven has a reviewed managed-instance path.
	if p.Provider == "aiven" {
		modes = append([]string{"new-instance"}, modes...)
		fields["mode"] = map[string]any{"type": "string", "enum": modes}
		fields["instance"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"project", "service", "plan", "cloud"}, "properties": map[string]any{
			"project": map[string]any{"type": "string"}, "service": map[string]any{"type": "string"},
			"plan": map[string]any{"type": "string"}, "cloud": map[string]any{"type": "string"},
		}}
	}
	// Existing-connection modes delegate to the engine packages; managed Aiven returns service identity only.
	names := []string{"serviceId", "host", "url", "port", "username", "password", "database", "keyPrefix", "caCertificatePEM"}
	properties := map[string]any{}
	// Publish static output names for the modes that can return each value.
	for _, name := range names {
		properties[name] = map[string]any{"type": "string"}
	}
	return workflow.Capability{
		Version:       1,
		Name:          p.Provider,
		ConfigSchema:  map[string]any{"type": "object", "additionalProperties": false, "required": []string{"engine", "mode"}, "properties": fields},
		SecretAliases: []string{"apiToken", "connection", "caCertificatePEM"},
		OutputSchema:  map[string]any{"type": "object", "additionalProperties": false, "properties": properties},
		Outputs:       names,
	}
}
