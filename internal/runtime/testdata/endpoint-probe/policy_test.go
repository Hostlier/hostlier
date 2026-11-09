package endpointprobe

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/quic-go/quic-go/http3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// policyFixture requires deliberate opt-in before exercising an explicit policy deployment.
func policyFixture(t *testing.T) (context.Context, string) {
	t.Helper()
	// Older unrestricted protocol fixtures cannot satisfy default-deny assertions.
	if os.Getenv("HOSTLIER_POLICY_E2E") != "1" {
		t.Skip("explicit traffic policy fixture required")
	}
	return fixture(t)
}

// TestPolicyHTTPDenials proves invalid verbs, path variants and fake upgrades never reach a catch-all backend.
func TestPolicyHTTPDenials(t *testing.T) {
	ctx, base := policyFixture(t)
	cases := []struct{ method, path string }{{"GET", "/unknown"}, {"GET", "/health/admin"}, {"POST", "/health"}, {"POST", "/sse"}, {"PUT", "/mcp"}, {"GET", "/mcp/"}, {"GET", "/MCP"}, {"GET", "/ws"}, {"GET", "/quic"}}
	// The fixture backend serves arbitrary paths, so every rejection must come from the edge.
	for _, item := range cases {
		response := request(t, ctx, item.method, base+item.path, nil, "")
		body, err := io.ReadAll(response.Body)
		// An application response or redirect would show policy fallthrough or path rewriting.
		if err != nil || response.StatusCode != 403 || strings.Contains(string(body), "revision-one") {
			t.Fatalf("%s %s: status=%d body=%q error=%v", item.method, item.path, response.StatusCode, body, err)
		}
		t.Logf("denied %s %s: HTTP 403", item.method, item.path)
	}
}

// TestPolicyHTTP3RequiresQUIC proves the per-route restriction using an actual UDP-only client.
func TestPolicyHTTP3RequiresQUIC(t *testing.T) {
	ctx, base := policyFixture(t)
	transport := &http3.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	defer transport.Close()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/quic", nil)
	// Only the explicitly selected certificate-verified fixture destination is requested.
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(req)
	// There is no TCP fallback in this transport.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	// The same path is denied over TCP by TestPolicyHTTPDenials.
	if err != nil || response.ProtoMajor != 3 || response.StatusCode != 200 || string(body) != "revision-one\n" {
		t.Fatalf("HTTP/3 %s status=%d body=%q error=%v", response.Proto, response.StatusCode, body, err)
	}
	t.Log("QUIC-only path accepted over real HTTP/3")
}

// policyEngineConnection opens one certificate-verified channel shared by SDK and Bridge services.
func policyEngineConnection(t *testing.T) *grpc.ClientConn {
	t.Helper()
	_, _ = policyFixture(t)
	target := os.Getenv("HOSTLIER_ENGINE_GRPC")
	// An explicit Engine address prevents accidentally probing the generic health fixture.
	if target == "" {
		t.Fatal("Engine gRPC destination required")
	}
	connection, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
	// Certificate validation stays enabled for every policy assertion.
	if err != nil {
		t.Fatal(err)
	}
	// Each test releases its authenticated TLS channel even when an assertion fails.
	t.Cleanup(func() { connection.Close() })
	return connection
}

// TestPolicyEngineSDKAndBridgeRouting distinguishes real Engine handlers from proxy permission errors.
func TestPolicyEngineSDKAndBridgeRouting(t *testing.T) {
	ctx, _ := policyFixture(t)
	connection := policyEngineConnection(t)
	err := connection.Invoke(ctx, "/engine.v1.EngineService/Connect", &emptypb.Empty{}, &emptypb.Empty{})
	// An empty SDK request must reach Engine's actual UUID validation handler.
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "SDK app ID must be a valid UUID") {
		t.Fatalf("SDK handler: %v", err)
	}
	t.Log("SDK Connect reached Engine: INVALID_ARGUMENT for missing app UUID")
	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/fused.bridge.v1.BridgeService/Connect")
	// Bridge uses bidirectional gRPC on the same authority and container port.
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	err = stream.RecvMsg(&emptypb.Empty{})
	// Bridge owns installation authentication independently of SDK runtime tokens.
	if status.Code(err) != codes.Unauthenticated || !strings.Contains(status.Convert(err).Message(), "bridge credential required") {
		t.Fatalf("Bridge handler: %v", err)
	}
	t.Log("Bridge Connect reached Engine: UNAUTHENTICATED for missing Bridge credential")
}

// TestPolicySDKWebhookRoute keeps the generated receiver's bidirectional RPC available in the final allowlist.
func TestPolicySDKWebhookRoute(t *testing.T) {
	ctx, _ := policyFixture(t)
	connection := policyEngineConnection(t)
	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/engine.v1.EngineService/SubscribeWebhooks")
	// The same TLS authority serves SDK unary, SDK bidirectional and Bridge bidirectional methods.
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	err = stream.RecvMsg(&emptypb.Empty{})
	// Missing subscription data must reach Engine's own first-frame validation, never create a consumer.
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "expected initial subscribe message") {
		t.Fatalf("SDK webhook handler: %v", err)
	}
	t.Log("SDK SubscribeWebhooks reached Engine's initial subscription validation")
}

// TestPolicyEngineDeniesUnlistedRPCs proves exact, case-sensitive policies cannot fall through to a root route.
func TestPolicyEngineDeniesUnlistedRPCs(t *testing.T) {
	ctx, _ := policyFixture(t)
	connection := policyEngineConnection(t)
	paths := []string{"/engine.v1.EngineService/Unknown", "/engine.v1.EngineService/connect", "/Engine.v1.EngineService/Connect", "/engine.v1.EngineService/Connect/extra", "/fused.bridge.v1.BridgeService/Unknown", "/fused.bridge.v1.BridgeService/Connect/extra", "/grpc.health.v1.Health/Check"}
	// Unknown methods, case changes and descendants must never bypass the explicit service contract.
	for _, path := range paths {
		err := connection.Invoke(ctx, path, &emptypb.Empty{}, &emptypb.Empty{})
		// Engine's UNIMPLEMENTED or authentication errors would indicate policy fallthrough.
		if status.Code(err) != codes.PermissionDenied || !strings.Contains(status.Convert(err).Message(), "Hostlier gRPC policy") {
			t.Fatalf("%s: %v", path, err)
		}
		t.Logf("denied %s: PERMISSION_DENIED", path)
	}
}
