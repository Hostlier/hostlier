package model

import "strings"

// FleetPath extracts one explicitly selected enrolled host and its versioned API path.
func FleetPath(path string) (string, string, bool) {
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// Apply this check before granting authority or advancing protocol state.
	if len(p) < 5 || p[0] != "v1" || p[1] != "hosts" || p[3] != "proxy" || p[2] == "" {
		return "", "", false
	}
	return p[2], "/v1/" + strings.Join(p[4:], "/"), true
}

// FleetRoute limits delegated host authority to workload operations, excluding trust administration.
func FleetRoute(path string) bool {
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// Fail closed when the required identity, scope or format does not match.
	if len(p) < 2 || p[0] != "v1" || strings.Contains(path, "..") {
		return false
	}
	// History and platform discovery are read-only. Package administration stays on the owning controller.
	if path == "/v1/telemetry/history" || path == "/v1/packages/platform" {
		return true
	}
	allowed := map[string]bool{"backup-ticks": true, "recovery-review": true, "backup-destinations": true, "backup-policies": true, "recovery-jobs": true, "upgrades": true, "backup-imports": true, "host-state": true, "status": true, "doctor": true, "logs": true, "plans": true, "planning": true, "resource-plans": true, "resources": true, "backups": true, "secrets": true, "apps": true, "operations": true, "artifacts": true, "workflows": true, "domains": true}
	return allowed[p[1]]
}
