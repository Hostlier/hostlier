package nginx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// TestRenderSegmentRouting checks stripping, query-safe redirects, stream proxying and scale-to-zero reservations.
func TestRenderSegmentRouting(t *testing.T) {
	project := model.Project{Name: "app", Route: "app.example.com/api", Proxy: &model.Proxy{StripPrefix: true}}
	data, err := Render(Options{}, "controller.example.com", "127.0.0.1:7463", []Route{{Project: project, Upstreams: []string{"127.0.0.1:3000"}}})
	// Rendering must accept validated controller and application routes.
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	// These directives jointly preserve prefix boundaries, TLS, streaming and WebSocket upgrades.
	for _, expected := range []string{"location = /api", "location ^~ /api/", "proxy_pass http://127.0.0.1:3000/;", "proxy_buffering off", "proxy_set_header Upgrade", "ssl_certificate", "$is_args$args"} {
		// Failure identifies the missing behavior rather than comparing irrelevant whitespace.
		if !strings.Contains(text, expected) {
			t.Fatal(expected, text)
		}
	}
}

// TestControllerBodyLimit admits encoded artifacts and planning requests through either Nginx mode.
func TestControllerBodyLimit(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		options := Options{Gateway: gateway, ControllerBodyLimitFile: filepath.Join(t.TempDir(), "body-limit")}
		controller, err := hostServer(options, "controller.example.com", "controller.example.com", "127.0.0.1:7463", nil)
		if err != nil {
			t.Fatal(err)
		}
		app, err := hostServer(options, "app.example.com", "controller.example.com", "127.0.0.1:7463", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, directive := range []string{
			"client_max_body_size 1m;",
			`location ~ "^/v1/artifacts/[a-f0-9]{64}/chunks$" { client_max_body_size 2m;`,
			`location ~ "^/v1/(hosts/[^/]+/proxy/)?(plans|planning)$" { client_max_body_size 2m;`,
			"location / {",
		} {
			if !strings.Contains(controller, directive) {
				t.Fatalf("gateway=%t: missing %q", gateway, directive)
			}
		}
		if strings.Count(controller, "client_max_body_size 2m;") != 2 || strings.Contains(app, "client_max_body_size") {
			t.Fatalf("gateway=%t: unexpected body limit outside controller payload routes", gateway)
		}
	}
}

// TestControllerBodyLimitOverride verifies one explicit limit applies to HTTP and RPC locations.
func TestControllerBodyLimitOverride(t *testing.T) {
	for _, tc := range []struct{ config, limit string }{
		{"0\n", "0"},
		{"32\n", "32m"},
		{"1\n", "1m"},
		{"-1\n", ""},
		{"32m\n", ""},
		{"\n", ""},
	} {
		t.Run(strings.TrimSpace(tc.config), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "body-limit")
			if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			for _, gateway := range []bool{false, true} {
				options := Options{Gateway: gateway, ControllerBodyLimitFile: path}
				controller, err := hostServer(options, "controller.example.com", "controller.example.com", "127.0.0.1:7463", nil)
				if tc.limit == "" {
					if err == nil {
						t.Fatalf("gateway=%t: invalid cap accepted", gateway)
					}
					continue
				}
				directive := "client_max_body_size " + tc.limit + ";"
				if err != nil || !strings.Contains(controller, directive) || strings.Count(controller, "client_max_body_size ") != 1 {
					t.Fatalf("gateway=%t: expected all controller locations to inherit %q, got %s (%v)", gateway, directive, controller, err)
				}
			}
		})
	}
}

// TestActivationRollback leaves the previous file and routing active when candidate validation or reload fails.
func TestActivationRollback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")
	os.WriteFile(path, []byte("previous"), 0600)
	calls := 0
	// Order specific routes before their parent locations.
	options := Options{Directory: dir, Run: func(_ context.Context, _ string, args ...string) error {
		calls++
		// First configuration validation succeeds; the first reload fails; rollback succeeds.
		if calls == 2 {
			return errors.New("reload rejected")
		}
		return nil
	}}
	// A reload error must remain visible even after successful configuration rollback.
	if err := Activate(context.Background(), options, []byte("candidate")); err == nil {
		t.Fatal("reload failure hidden")
	}
	data, _ := os.ReadFile(path)
	// The previous validated file is the only publishable recovery state.
	if string(data) != "previous" || calls != 3 {
		t.Fatal(string(data), calls)
	}
}

// TestCertbotFailureDetail keeps the actionable ACME result without exposing routine log output.
func TestCertbotFailureDetail(t *testing.T) {
	output := "Account: private\nDetail: DNS problem: NXDOMAIN for hostlier.example.com\nSome challenges have failed.\n"
	// Report the challenge failure rather than the final generic Certbot line.
	if got := certbotFailureDetail(output); got != "DNS problem: NXDOMAIN for hostlier.example.com" {
		t.Fatal(got)
	}
	// Control characters in a remote response must not enter terminal output.
	if got := certbotFailureDetail("Detail: timeout\x1b[31m\n"); got != "timeout[31m" {
		t.Fatal(got)
	}
}
