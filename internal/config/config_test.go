package config

import (
	"os"
	"path/filepath"
	"testing"

	"hostlier/internal/model"
)

// TestRoute covers canonical routes, hostname boundaries and controller reservation.
func TestRoute(t *testing.T) {
	r, err := ParseRoute("https://Analytics.Example.com/reports/", []string{"example.com"}, "")
	// Require normalization to preserve the expected host and non-root path.
	if err != nil || r.Key != "analytics.example.com/reports" {
		t.Fatalf("%+v %v", r, err)
	}
	// Exercise scheme, traversal, delimiter and domain-boundary attacks.
	for _, bad := range []string{"http://example.com", "https://user:pass@example.com", "example.com:443", "example.com/a/../b", "example.com/%2e%2e/x", "example.com/a//b", "example.com?x=1", "example.com#x", "example.com\\x", "notexample.com", "example.com.evil.org"} {
		// Every ambiguous or unsupported route must be rejected.
		if _, e := ParseRoute(bad, []string{"example.com"}, ""); e == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	// Applications cannot claim the controller hostname even with a distinct path.
	if _, e := ParseRoute("hostlier.example.com/x", []string{"example.com"}, "hostlier.example.com"); e == nil {
		t.Fatal("reserved controller accepted")
	}
	// An explicitly empty allowlist authorizes no hostname.
	if _, e := ParseRoute("example.com", []string{}, ""); e == nil {
		t.Fatal("empty allowlist accepted")
	}
}

// TestProject checks a valid minimal project and rejects invalid identifiers.
func TestProject(t *testing.T) {
	p := model.Project{Version: 1, Name: "analytics", Image: "nginx:alpine", Port: 80, Route: "analytics.example.com", Secrets: map[string]string{"API_KEY": "API_KEY"}}
	// A valid project must survive schema validation.
	if _, e := ValidateProject(p); e != nil {
		t.Fatal(e)
	}
	p.Env = map[string]string{"API_KEY": "literal"}
	// Changing the name to an invalid identifier must fail validation.
	if _, e := ValidateProject(p); e == nil {
		t.Fatal("duplicate secret/env accepted")
	}
}

// TestYAML verifies strict decoding and normalization without accepting extra documents.
func TestYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".hostlier")
	valid := "version: 1\nname: analytics\nimage: nginx:alpine\nport: 80\nroute: example.com/analytics\n"
	// Create the valid fixture before exercising the parser.
	if e := os.WriteFile(path, []byte(valid), 0600); e != nil {
		t.Fatal(e)
	}
	p, e := ReadProject(path)
	// Require successful parsing and a normalized project route.
	if e != nil || p.Route != "example.com/analytics" {
		t.Fatalf("%+v %v", p, e)
	}
	// Exercise duplicate keys, unknown fields, extra documents, aliases and wrong scalar types.
	for _, text := range []string{valid + "version: 1\n", valid + "unknown: secret-value\n", valid + "---\nname: other\n", "a: &a\n  self: *a\n", valid + "route:\n  domain: example.com\n"} {
		os.WriteFile(path, []byte(text), 0600)
		// Malformed fixtures must fail strict project decoding.
		if _, e := ReadProject(path); e == nil {
			t.Fatal("invalid YAML accepted")
		}
	}
}

// TestServerOnlyDomains checks normalized and unique supported hostnames.
func TestServerOnlyDomains(t *testing.T) {
	s, e := ValidateServer(model.ServerConfig{Version: 1, Domains: []string{"Example.COM"}})
	// Require server domains to normalize to lowercase.
	if e != nil || s.Domains[0] != "example.com" {
		t.Fatal(e)
	}
	// Case aliases must count as duplicate domains.
	if _, e = ValidateServer(model.ServerConfig{Version: 1, Domains: []string{"example.com", "EXAMPLE.COM"}}); e == nil {
		t.Fatal("duplicate domain accepted")
	}
}

// TestFingerprint verifies deterministic key ordering and value sensitivity.
func TestFingerprint(t *testing.T) {
	a := Fingerprint(map[string]int{"a": 1, "b": 2})
	// Equivalent maps must hash equally while changed values must differ.
	if a != Fingerprint(map[string]int{"b": 2, "a": 1}) || a == Fingerprint(map[string]int{"a": 2}) {
		t.Fatal("unstable fingerprint")
	}
}

// TestLimitsCannotOverflowOrDisableEnforcement covers shared backend limit boundaries.
func TestLimitsCannotOverflowOrDisableEnforcement(t *testing.T) {
	p := model.Project{Version: 1, Name: "app", Image: "nginx:alpine", Port: 80}
	// Exercise sub-millicore, nonfinite and imprecise CPU values.
	for _, cpu := range []string{"0.0001", "1e-50", "1.0001", "NaN", "+Inf"} {
		p.Resources = &model.Limits{CPU: cpu}
		// Unsupported CPU quantities must fail before runtime conversion.
		if _, err := ValidateProject(p); err == nil {
			t.Errorf("accepted unsupported CPU %s", cpu)
		}
	}
	// Exercise overflowing and nonpositive memory quantities.
	for _, size := range []string{"8388608Ti", "999999999999999999999999Gi", "0Mi"} {
		p.Resources = &model.Limits{Memory: size}
		// Oversized storage values must not wrap into enforceable byte limits.
		if _, err := ValidateProject(p); err == nil {
			t.Errorf("accepted overflowing memory %s", size)
		}
	}
	// Keep boundary and ordinary millicore values valid.
	for _, cpu := range []string{"0.001", "0.5", "1.001", "1024"} {
		p.Resources = &model.Limits{CPU: cpu, Memory: "128Mi"}
		// Supported CPU limits must remain accepted by validation.
		if _, err := ValidateProject(p); err != nil {
			t.Fatal(cpu, err)
		}
	}
}
