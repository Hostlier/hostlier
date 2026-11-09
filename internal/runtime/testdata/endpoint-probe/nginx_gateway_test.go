package endpointprobe

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestNginxGatewayOverwritesClientClaims verifies forged metadata cannot change backend admission.
func TestNginxGatewayOverwritesClientClaims(t *testing.T) {
	ctx, base := policyFixture(t)
	// Direct Caddy or standalone Nginx tests do not claim to exercise a managed gateway backend.
	if os.Getenv("HOSTLIER_NGINX_GATEWAY_E2E") != "1" {
		t.Skip("explicit managed Nginx gateway fixture required")
	}
	headers := http.Header{"X-Hostlier-Gateway-Method": {"CONNECT"}, "X-Hostlier-Gateway-Protocol": {"HTTP/3"}, "X-Hostlier-Gateway-Scheme": {"http"}}
	connection, _, err := websocket.Dial(ctx, base+"/ws", &websocket.DialOptions{HTTPHeader: headers})
	// This GET/H1-only resource would be denied by Nginx if forged CONNECT/H3 values survived.
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	// Real bidirectional traffic proves both gateway and Nginx accepted the actual wire contract.
	if err := connection.Write(ctx, websocket.MessageText, []byte("gateway-metadata-proof")); err != nil {
		t.Fatal(err)
	}
	_, payload, err := connection.Read(ctx)
	// An allowed handshake alone does not prove bytes traverse the backend.
	if err != nil || string(payload) != "gateway-metadata-proof" {
		t.Fatalf("echo=%q error=%v", payload, err)
	}
	t.Log("forged CONNECT/H3 metadata overwritten; Nginx admitted only the actual GET/HTTP1 WebSocket contract")
}

// TestNginxGatewayBackendIsRequired proves the gateway never bypasses Nginx to reach an application.
func TestNginxGatewayBackendIsRequired(t *testing.T) {
	ctx, base := policyFixture(t)
	// Only the named disposable guest may temporarily stop its newly managed private backend.
	if os.Getenv("HOSTLIER_NGINX_GATEWAY_E2E") != "1" {
		t.Skip("explicit managed Nginx gateway fixture required")
	}
	host, _ := os.Hostname()
	marker, err := os.ReadFile("/root/hostlier-cold-cli-e2e/fixture")
	// No production or developer-host service may be affected by this outage test.
	if err != nil || host != "lima-hostlier-cli-deploy" || string(marker) != "hostlier-cold-cli-e2e-v1\n" || os.Geteuid() != 0 {
		t.Fatal("wrong gateway outage fixture")
	}
	defer restoreGatewayBackend(t)
	// Public Caddy and the controller remain running; only the private Nginx hop is stopped.
	if err := exec.CommandContext(ctx, "systemctl", "stop", "hostlier-nginx-gateway.service").Run(); err != nil {
		t.Fatal(err)
	}
	// Each pinned transport must fail through the actual gateway instead of silently routing directly.
	for _, version := range []int{2, 3} {
		transport := extendedTransport(t, version)
		path := map[int]string{2: "/ws-h2", 3: "/ws-h3"}[version]
		response, err := transport.RoundTrip(extendedRequest(t, ctx, base+path, version, "websocket", http.NoBody))
		// A real gateway error still completes the selected TLS/protocol negotiation.
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		// Successful application bytes while Nginx is down would demonstrate a forbidden bypass.
		if !gatewayOutageResponse(response, version) {
			t.Fatalf("Nginx stopped: HTTP/%d returned %s %d", version, response.Proto, response.StatusCode)
		}
		t.Logf("Nginx stopped: HTTP/%d WebSocket returned 502; no direct application bypass", version)
	}
}

// gatewayOutageResponse distinguishes a negotiated proxy failure from a fallback or application response.
func gatewayOutageResponse(response *http.Response, version int) bool {
	return response.StatusCode == 502 && response.ProtoMajor == version
}

// restoreGatewayBackend always restores the managed backend using an independent bounded cleanup context.
func restoreGatewayBackend(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemctl", "start", "hostlier-nginx-gateway.service").CombinedOutput()
	// Failed restoration is a test failure that must remain visible even after an earlier assertion.
	if err != nil {
		t.Errorf("restore Nginx gateway: %v %s", err, strings.TrimSpace(string(output)))
	}
}
