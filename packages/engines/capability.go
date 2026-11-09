package engines

import "hostlier/pkg/workflow"

// Capability describes the public settings and available outputs of this engine.
func (p Package) Capability() workflow.Capability {
	required := []string{"url", "host", "port", "username", "password"}
	names := append([]string{"caCertificatePEM"}, required...)
	modes := []string{"new-instance", "existing"}
	fields := map[string]any{
		"storage": map[string]any{"type": "string", "description": "Storage size for a new instance."},
	}
	// Existing Redis namespaces return a key prefix. PostgreSQL returns the database name.
	if p.Engine == "postgres" {
		modes = []string{"new-instance", "new-database"}
		fields["database"] = map[string]any{"type": "string", "description": "Optional database name for new-database mode; omitted names are derived from the stable workflow key. New instances use app."}
		names = append(names, "database")
	} else {
		fields["keyPrefix"] = map[string]any{"type": "string", "description": "Key prefix for an existing Redis instance."}
		names = append(names, "keyPrefix")
	}
	fields["mode"] = map[string]any{"type": "string", "enum": modes}
	return workflow.Capability{
		Version: 1, Name: p.Engine, Inspect: true,
		ConfigSchema:  map[string]any{"type": "object", "additionalProperties": false, "required": []string{"mode"}, "properties": fields},
		SecretAliases: []string{"connection", "caCertificatePEM"},
		OutputSchema:  map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": outputProperties(names)},
		Outputs:       names,
	}
}

// outputProperties describes output types without producing credentials or runtime effects.
func outputProperties(names []string) map[string]any {
	properties := map[string]any{}
	// Native engine bindings use string values, including their port.
	for _, name := range names {
		properties[name] = map[string]any{"type": "string"}
	}
	return properties
}
