package model

import "strings"

// UpgradeImage limits the first upgrade workflows to exact images exercised by the integration suite.
func UpgradeImage(engine, version string) string {
	return map[string]string{
		"postgres:17":   "postgres@sha256:266d3c257594e195b309ae9d1805e54c788f1d7b375f56bf6c367aac6c917e7b",
		"redis:8.2":     "redis@sha256:489a769e0a54dc245896132a15c72d2bc7296719954da99398f43a3877e5f8eb",
		"neo4j:2025.01": "neo4j@sha256:e9be37e336d23876723a01c6ca1e494ca54522326ad7749b0b46c57d19fd6b8f",
	}[engine+":"+version]
}

// ApprovedDatabaseImage permits only shipped, immutable upgrade images, never arbitrary database executables.
func ApprovedDatabaseImage(engine, image string) bool {
	// Each supported transition has one independently pinned destination.
	for _, version := range []string{"17", "8.2", "2025.01"} {
		// Advance only after the required recovery invariant holds.
		if candidate := UpgradeImage(engine, version); candidate != "" && candidate == image {
			return true
		}
	}
	return false
}

// SupportedTransition checks actual native versions after provisioning and before fencing the source.
func SupportedTransition(engine, source, target string) bool {
	// Select the explicit recovery operation or native engine behavior.
	switch engine {
	// PostgreSQL uses a logical dump into a fresh next-major database.
	case "postgres":
		return strings.HasPrefix(source, "16.") && strings.HasPrefix(target, "17.")
	// Redis core data uses an RDB restore with explicit TTL-aware content validation.
	case "redis":
		return strings.HasPrefix(source, "7.4.") && strings.HasPrefix(target, "8.2.")
	// Neo4j requires its LTS checkpoint before changing release series.
	case "neo4j":
		return strings.HasPrefix(source, "5.26.") && strings.HasPrefix(target, "2025.01.")
	}
	return false
}
