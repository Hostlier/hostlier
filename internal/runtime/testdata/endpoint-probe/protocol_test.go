package endpointprobe

import (
	"bufio"
	"context"
	"crypto/tls"
	"github.com/coder/websocket"
	"github.com/quic-go/quic-go/http3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// fixture confines traffic to an explicitly selected, certificate-verified disposable deployment.
func fixture(t *testing.T) (context.Context, string) {
	t.Helper()
	// Ordinary builds never assume a running VM.
	if os.Getenv("HOSTLIER_PROTOCOL_E2E") != "1" {
		t.Skip("explicit protocol fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	base := os.Getenv("HOSTLIER_PROTOCOL_URL")
	// Never silently substitute an insecure test destination.
	if !strings.HasPrefix(base, "https://") {
		t.Fatal("HTTPS destination required")
	}
	return ctx, base
}

// request preserves methods, streaming bodies and session headers through the edge.
func request(t *testing.T, ctx context.Context, method, target string, body io.Reader, session string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	// Invalid URLs cannot change the target silently.
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", session)
	res, err := http.DefaultClient.Do(req)
	// TLS and routing failures are genuine test failures.
	if err != nil {
		t.Fatal(err)
	}
	// Every response is released even when assertions fail.
	t.Cleanup(func() { res.Body.Close() })
	return res
}

// TestHTTP requires bytes from the actual mounted revision file.
func TestHTTP(t *testing.T) {
	ctx, base := fixture(t)
	res := request(t, ctx, "GET", base+"/health", nil, "")
	data, err := io.ReadAll(res.Body)
	// The expected body proves the request reached this workload revision.
	if err != nil || res.StatusCode != 200 || string(data) != "revision-one\n" {
		t.Fatalf("HTTP %d %q %v", res.StatusCode, data, err)
	}
}

// events requires an early first chunk and the intact delayed second chunk.
func events(t *testing.T, res *http.Response, started time.Time) {
	t.Helper()
	reader := bufio.NewReader(res.Body)
	first, err := reader.ReadString('\n')
	// Buffering until completion would exceed the first-event budget.
	if err != nil || !strings.HasPrefix(first, "event: message") || time.Since(started) > 800*time.Millisecond {
		t.Fatalf("first event %q %s %v", first, time.Since(started), err)
	}
	rest, err := io.ReadAll(reader)
	// Both event payloads must survive incremental forwarding.
	if err != nil || !strings.Contains(string(rest), `"sequence":1`) || !strings.Contains(string(rest), `"sequence":2`) {
		t.Fatalf("events %q %v", rest, err)
	}
}

// TestSSE exercises incremental server-sent events over ordinary HTTP.
func TestSSE(t *testing.T) {
	ctx, base := fixture(t)
	start := time.Now()
	events(t, request(t, ctx, "GET", base+"/sse", nil, ""), start)
}

// TestWebSocket verifies upgrade and bidirectional masked framing with a standard client.
func TestWebSocket(t *testing.T) {
	ctx, base := fixture(t)
	conn, _, err := websocket.Dial(ctx, base+"/ws", nil)
	// An ordinary HTTP response cannot satisfy a WebSocket handshake.
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// Echo traffic crosses both directions after upgrade.
	if err = conn.Write(ctx, websocket.MessageText, []byte("hostlier-live-websocket")); err != nil {
		t.Fatal(err)
	}
	kind, data, err := conn.Read(ctx)
	// The library checks framing and the test checks application bytes.
	if err != nil || kind != websocket.MessageText || string(data) != "hostlier-live-websocket" {
		t.Fatalf("echo %q %v", data, err)
	}
}

// TestStreamableHTTP verifies initialize, session headers, POST/GET event streams and DELETE cleanup.
func TestStreamableHTTP(t *testing.T) {
	ctx, base := fixture(t)
	res := request(t, ctx, "POST", base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`), "")
	session := res.Header.Get("Mcp-Session-Id")
	data, err := io.ReadAll(res.Body)
	// Initialize must retain its JSON response and session identifier.
	if err != nil || session != "hostlier-probe-session" || !strings.Contains(string(data), `"protocolVersion"`) {
		t.Fatalf("initialize %q %q %v", session, data, err)
	}
	start := time.Now()
	events(t, request(t, ctx, "POST", base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call"}`), session), start)
	start = time.Now()
	events(t, request(t, ctx, "GET", base+"/mcp", nil, session), start)
	// DELETE must reach the application with the same session header.
	if res = request(t, ctx, "DELETE", base+"/mcp", nil, session); res.StatusCode != 204 {
		t.Fatal(res.Status)
	}
}

// TestDuplex requires a response while the request body is still open.
func TestDuplex(t *testing.T) {
	ctx, base := fixture(t)
	reader, writer := io.Pipe()
	defer writer.Close()
	// Keep the upload open until its first echo has been consumed.
	go func() { _, _ = writer.Write([]byte("first\n")) }()
	res := request(t, ctx, "POST", base+"/duplex", reader, "")
	first, err := bufio.NewReader(res.Body).ReadString('\n')
	// Request buffering would deadlock until the context deadline.
	if err != nil || first != "first\n" {
		t.Fatalf("duplex %q %v", first, err)
	}
}

// TestNativeGRPC checks unary protobuf framing and a server stream with final trailers.
func TestNativeGRPC(t *testing.T) {
	ctx, _ := fixture(t)
	conn, err := grpc.NewClient(os.Getenv("HOSTLIER_PROTOCOL_GRPC"), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
	// Native gRPC always uses verified TLS and HTTP/2.
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := grpc_health_v1.NewHealthClient(conn)
	res, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	// A typed successful result proves gRPC framing and grpc-status survived.
	if err != nil || res.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("check %v %v", res, err)
	}
	stream, err := client.Watch(ctx, &grpc_health_v1.HealthCheckRequest{})
	// Streaming setup must succeed independently of unary routing.
	if err != nil {
		t.Fatal(err)
	}
	grpcEvents(t, stream)
}

// grpcEvents requires early delivery, the second message and clean EOF validated by grpc-go.
func grpcEvents(t *testing.T, stream grpc.ServerStreamingClient[grpc_health_v1.HealthCheckResponse]) {
	t.Helper()
	start := time.Now()
	first, err := stream.Recv()
	// A buffered stream cannot delay its first message until completion.
	if err != nil || first.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING || time.Since(start) > 800*time.Millisecond {
		t.Fatalf("first %v %v", first, err)
	}
	second, err := stream.Recv()
	// The delayed second message must arrive without timeout or truncation.
	if err != nil || second.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("second %v %v", second, err)
	}
	// grpc-go verifies final grpc-status before reporting EOF.
	if _, err = stream.Recv(); err != io.EOF {
		t.Fatalf("trailers %v", err)
	}
}

// TestHTTP3 uses a QUIC-only transport so TCP fallback cannot satisfy the check.
func TestHTTP3(t *testing.T) {
	ctx, base := fixture(t)
	transport := &http3.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	defer transport.Close()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/health", nil)
	// Preserve the explicit public destination.
	if err != nil {
		t.Fatal(err)
	}
	res, err := transport.RoundTrip(req)
	// A genuine UDP/TLS handshake is required rather than an Alt-Svc header.
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	// QUIC must route to the same mounted revision as TCP transports.
	if err != nil || res.ProtoMajor != 3 || string(data) != "revision-one\n" {
		t.Fatalf("HTTP/3 %s %q %v", res.Proto, data, err)
	}
}

// TestEngineGRPC reaches the separate Engine endpoint's actual authentication handler.
func TestEngineGRPC(t *testing.T) {
	ctx, _ := fixture(t)
	target := os.Getenv("HOSTLIER_ENGINE_GRPC")
	// Generic routing checks can run before the licensed Engine is available.
	if target == "" {
		t.Skip("Engine endpoint not selected")
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
	// Engine certificate validation stays enabled.
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	err = conn.Invoke(ctx, "/engine.v1.EngineService/Connect", &emptypb.Empty{}, &emptypb.Empty{})
	// Engine must reject missing credentials at its own handler, not fail at the proxy.
	if status.Code(err) != codes.Unauthenticated && status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Engine Connect: %v", err)
	}
}

// TestEngineHTTP3 reaches the actual Engine health handler over a QUIC-only connection.
func TestEngineHTTP3(t *testing.T) {
	ctx, _ := fixture(t)
	target := os.Getenv("HOSTLIER_ENGINE_HTTP")
	// The protocol fixture can run without a licensed Engine deployment.
	if target == "" {
		t.Skip("Engine endpoint not selected")
	}
	transport := &http3.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	defer transport.Close()
	req, err := http.NewRequestWithContext(ctx, "GET", target+"/health", nil)
	// Only an explicit HTTPS Engine address is accepted.
	if err != nil || !strings.HasPrefix(target, "https://") {
		t.Fatal("invalid Engine HTTPS address")
	}
	response, err := transport.RoundTrip(req)
	// Certificate validation and a real UDP handshake must succeed.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	// The response must be Engine's health document rather than another fixture route.
	if err != nil || response.ProtoMajor != 3 || response.StatusCode != 200 || !strings.Contains(string(data), `"status":"ok"`) {
		t.Fatalf("Engine HTTP/3 %s %q %v", response.Proto, data, err)
	}
}
