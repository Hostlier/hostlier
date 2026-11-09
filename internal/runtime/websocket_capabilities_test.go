package runtime

import (
	"bytes"
	"context"
	"hostlier/internal/model"
	"testing"
)

// TestConnectSettingsRequiresExplicitServerPermission guards the real HTTP/2 feature negotiation boundary.
func TestConnectSettingsRequiresExplicitServerPermission(t *testing.T) {
	// The payload advertises SETTINGS_ENABLE_CONNECT_PROTOCOL=1 on connection stream zero.
	enabled := []byte{0, 0, 6, 4, 0, 0, 0, 0, 0, 0, 8, 0, 0, 0, 1}
	// Only the standardized explicit opt-in can satisfy the capability gate.
	if err := readConnectSetting(bytes.NewReader(enabled)); err != nil {
		t.Fatal(err)
	}
	// Absent, disabled, oversized and truncated settings must all fail closed.
	for _, data := range [][]byte{{0, 0, 0, 4, 0, 0, 0, 0, 0}, {0, 0, 6, 4, 0, 0, 0, 0, 0, 0, 8, 0, 0, 0, 0}, {1, 0, 0, 4, 0, 0, 0, 0, 0}, enabled[:12]} {
		// Invalid framing cannot be mistaken for extended CONNECT support.
		if err := readConnectSetting(bytes.NewReader(data)); err == nil {
			t.Fatalf("unsupported settings accepted: %x", data)
		}
	}
}

// TestNginxPreflightRejectsExtendedWebSocketBeforeMutation preserves the currently running deployment.
func TestNginxPreflightRejectsExtendedWebSocketBeforeMutation(t *testing.T) {
	r := &Runtime{options: Options{Proxy: "nginx"}}
	p := model.Project{Name: "app", Endpoints: map[string]model.Endpoint{"http": {Exposure: "public", Route: "https://app.example.com", Protocol: "http", HTTP: &model.HTTPPolicy{Routes: map[string]model.HTTPRoutePolicy{"socket": {Path: "/ws", Protocol: "websocket", Methods: []string{"CONNECT"}, HTTPVersions: []string{"h2"}}}}}}}
	// This check runs before pulling images or stopping the previous revision.
	if err := r.validateEdgeFeatures(context.Background(), p); err == nil {
		t.Fatal("unsupported extended CONNECT reached workload mutation")
	}
}

// TestExtendedCaddyMinimum rejects unsupported proxy versions before replacing a working workload.
func TestExtendedCaddyMinimum(t *testing.T) {
	// The known tested bridge and newer compatible releases satisfy the minimum.
	for value, want := range map[string]bool{"v2.11.7 h1:hash": true, "v2.12.0": true, "v2.6.2": false, "v2.11.6": false, "unknown": false} {
		// Parse actual command output, including its normal build metadata suffix.
		if got := extendedCaddyVersion(value); got != want {
			t.Fatalf("%s: got %v want %v", value, got, want)
		}
	}
}
