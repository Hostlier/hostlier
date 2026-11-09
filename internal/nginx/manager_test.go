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

// TestControllerBodyLimit keeps base64-encoded artifact chunks below the edge cap.
func TestControllerBodyLimit(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		options := Options{Gateway: gateway}
		controller, err := hostServer(options, "controller.example.com", "controller.example.com", "127.0.0.1:7463", nil)
		if err != nil {
			t.Fatal(err)
		}
		app, err := hostServer(options, "app.example.com", "controller.example.com", "127.0.0.1:7463", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(controller, "client_max_body_size 1m;") || !strings.Contains(controller, `location ~ "^/v1/artifacts/[a-f0-9]{64}/chunks$" { client_max_body_size 2m;`) || !strings.Contains(controller, "location / {") || strings.Count(controller, "client_max_body_size 2m;") != 1 || strings.Contains(app, "client_max_body_size") {
			t.Fatalf("gateway=%t: expected the larger limit only in the controller artifact location", gateway)
		}
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
