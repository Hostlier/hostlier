package localui

import (
	"hostlier/internal/model"
	"net/url"
)

// inventoryReadRoutes excludes payload exports, encrypted secrets and arbitrary remote proxy targets.
var inventoryReadRoutes = map[string]string{
	"/api/tunnels":     "/v1/tunnels",
	"/api/deployments": "/v1/apps", "/api/operations": "/v1/status",
	"/api/resources": "/v1/resources?observed=false", "/api/hosts": "/v1/hosts",
	"/api/recovery/jobs": "/v1/recovery-jobs", "/api/recovery/policies": "/v1/backup-policies",
	"/api/recovery/backups": "/v1/backups", "/api/recovery/destinations": "/v1/backup-destinations",
	"/api/access/clients": "/v1/clients", "/api/access/authorizations": "/v1/authorizations",
	"/api/access/approval": "/v1/human/status", "/api/settings/domains": "/v1/domains",
	"/api/settings/secrets": "/v1/secrets", "/api/settings/doctor": "/v1/doctor",
}

// domainDetailRoute maps one canonical identifier to its named read endpoint.
func domainDetailRoute(parts []string) (string, error) {
	name := url.PathEscape(parts[2])
	// Tunnel status contains no connector credential and never opens a transport.
	if parts[1] == "tunnels" && len(parts) == 3 {
		return "/v1/tunnels/" + name, nil
	}
	// Named resource views may inspect one resource or its already-redacted diagnostic tail.
	if parts[1] == "resources" {
		// Individual detail pages can request runtime status without probing the entire inventory.
		if len(parts) == 3 {
			return "/v1/resources/" + name, nil
		}
		// Log retrieval remains bound to that exact resource identity.
		if parts[3] == "logs" {
			return "/v1/resources/" + name + "/logs", nil
		}
	}
	// An enrolled host's state can only be read through the controller's existing fleet authority.
	if parts[1] == "hosts" && len(parts) == 3 {
		return "/v1/hosts/" + name + "/proxy/host-state", nil
	}
	// Recovery detail views inspect receipts, never backup contents or an export operation.
	if parts[1] == "recovery" && len(parts) == 3 {
		return "/v1/recovery-jobs/" + name, nil
	}
	return "", model.Err("NOT_FOUND", "Dashboard read endpoint not found")
}

// dashboardCollections are static embedded shell routes, independent of upstream authority.
var dashboardCollections = map[string]bool{"logs": true, "overview": true, "deployments": true, "operations": true, "observability": true, "resources": true, "hosts": true, "recovery": true, "access": true, "settings": true}

// dashboardDetails limits deep links to typed entities with canonical identifiers.
var dashboardDetails = map[string]bool{"tunnels": true, "deployments": true, "operations": true, "resources": true, "hosts": true, "recovery": true}
