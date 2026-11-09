package nginx

import (
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestGatewayRenderKeepsMetadataPrivateAndPreservesWirePolicies verifies the actual backend configuration.
func TestGatewayRenderKeepsMetadataPrivateAndPreservesWirePolicies(t *testing.T) {
	e := model.Endpoint{Exposure: "public", Protocol: "http", Route: "https://app.example.com", HTTP: &model.HTTPPolicy{Routes: map[string]model.HTTPRoutePolicy{"socket": {Path: "/ws", Protocol: "websocket", Methods: []string{"CONNECT"}, HTTPVersions: []string{"h2", "h3"}}}}}
	p := model.Project{Name: "app", Route: e.Route, Endpoint: &e}
	data, err := Render(Options{Gateway: true}, "", "", []Route{{Project: p, Upstreams: []string{"127.0.0.1:18081"}}})
	// Backend rendering implements the signed extended policies instead of claiming native Nginx support.
	if err != nil {
		t.Fatal(err)
	}
	// Both sockets are private and no public TLS/certificate service is introduced.
	for _, want := range []string{"listen 127.0.0.1:14443;", "listen 127.0.0.1:14444 http2;", "location = /ws", "$http_x_hostlier_gateway_method", "$http_x_hostlier_gateway_protocol", "X-Forwarded-Proto $http_x_hostlier_gateway_scheme", "HTTP/2|HTTP/3"} {
		// Missing guards would silently weaken protocol admission after conversion to HTTP/1.
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
	// A private backend cannot become a competing TLS edge or ACME listener.
	if strings.Contains(string(data), "ssl_certificate") || strings.Contains(string(data), "listen 80") {
		t.Fatal("gateway backend exposed a public certificate/listener")
	}
}

// TestGatewayRejectsPublicMetadataListeners prevents direct external injection of trusted transport fields.
func TestGatewayRejectsPublicMetadataListeners(t *testing.T) {
	// Explicit unsafe choices cannot fall back to standalone behavior.
	for _, address := range []string{"0.0.0.0:14443", "192.0.2.1:14443", "localhost:14443", "[::]:14443", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:http"} {
		_, err := Render(Options{Gateway: true, HTTPSAddress: address}, "", "", nil)
		// Only numeric loopback sockets can trust gateway-supplied metadata.
		if err == nil {
			t.Fatalf("public/unverifiable backend accepted: %s", address)
		}
	}
}
