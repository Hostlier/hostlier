package installer

import (
	"strings"
	"testing"
)

// TestGatewayInstallationAssetsMakeBothLayersExplicit verifies lifecycle ownership and the private trust boundary.
func TestGatewayInstallationAssetsMakeBothLayersExplicit(t *testing.T) {
	assets, err := InstallationAssets(Options{Domain: "example.com", Proxy: "nginx-gateway"})
	// A supported gateway declaration generates deployable assets without a patched Nginx binary.
	if err != nil {
		t.Fatal(err)
	}
	// Public TLS remains Caddy-owned; Nginx receives a distinct private service and both backend sockets.
	for _, part := range []string{"--proxy nginx-gateway", "hostlier-nginx-gateway.service"} {
		// Both startup ordering and the controller's selected mode must be reviewable.
		if !strings.Contains(assets.Service, part) {
			t.Fatal("missing controller gateway setting", part)
		}
	}
	// Private backend assets must not expose public TLS or a second ACME client.
	if !strings.Contains(assets.Nginx, "127.0.0.1:14443") || !strings.Contains(assets.Nginx, "127.0.0.1:14444") || assets.NginxRenewalService != "" {
		t.Fatal("invalid private gateway assets")
	}
}

// TestGatewayOptInRetainsOnlyOwnedCaddy permits the explicit private-layer addition without public ownership migration.
func TestGatewayOptInRetainsOnlyOwnedCaddy(t *testing.T) {
	// Legacy owned Caddy and current owned Caddy records have the same public service boundary.
	for _, previous := range []string{"", "caddy"} {
		plan := Plan{Options: Options{Proxy: "nginx-gateway"}}
		inspectProxyMigration(&plan, Installation{Proxy: previous, OwnedCaddy: true})
		// The remaining inspection still checks actual version compatibility and unowned private assets.
		if len(plan.Errors) != 0 {
			t.Fatal(plan.Errors)
		}
	}
	plan := Plan{Options: Options{Proxy: "nginx-gateway"}}
	inspectProxyMigration(&plan, Installation{Proxy: "caddy"})
	// Merely naming Caddy in an unowned record cannot authorize its adoption.
	if len(plan.Errors) == 0 {
		t.Fatal("gateway adopted an unowned Caddy installation")
	}
}

// TestGatewayCaddyVersionRequiresTestedBridge rejects incompatible distro releases before installation.
func TestGatewayCaddyVersionRequiresTestedBridge(t *testing.T) {
	cases := map[string]bool{"v2.6.2": false, "v2.11.6": false, "v2.11.7 h1:test": true, "v2.12.0": true, "unknown": false}
	// The package version and actual executable version can differ after a reviewed upgrade.
	for version, expected := range cases {
		// Unsupported binaries must not be accepted solely because the package record exists.
		if gatewayCaddyVersion(version) != expected {
			t.Fatal("incorrect gateway capability version decision", version)
		}
	}
}
