package endpointprobe

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
)

// extendedTransport selects a single wire protocol, prohibiting HTTP/1 or TCP fallback.
func extendedTransport(t *testing.T, version int) http.RoundTripper {
	t.Helper()
	// QUIC transport cannot satisfy a test by opening a TCP connection.
	if version == 3 {
		transport := &http3.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
		// Release QUIC connections even if a handshake or frame assertion fails.
		t.Cleanup(func() { transport.Close() })
		return transport
	}
	transport := &http2.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

// extendedRequest sets the actual HTTP/2 or HTTP/3 pseudo-header representation used by its transport.
func extendedRequest(t *testing.T, ctx context.Context, target string, version int, protocol string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "CONNECT", target, body)
	// Never replace an invalid target with another deployment or an insecure URL.
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	req.Header.Set("Sec-WebSocket-Version", "13")
	// RFC 9220 uses quic-go's Request.Proto representation of :protocol.
	if version == 3 {
		req.Proto = protocol
	} else { // RFC 8441 uses the HTTP/2 transport's pseudo-header field.
		req.Header.Set(":protocol", protocol)
	}
	return req
}

// TestWebSocketExtendedCONNECT verifies real negotiation, masked frames, control frames and stream reuse.
func TestWebSocketExtendedCONNECT(t *testing.T) {
	_, _ = policyFixture(t)
	// Each client transport is pinned to its protocol for the entire test.
	for _, version := range []int{2, 3} {
		// Subtest names identify which protocol's handshake or framing regressed.
		t.Run(map[int]string{2: "HTTP2", 3: "HTTP3"}[version], func(t *testing.T) {
			transport := extendedTransport(t, version)
			// A second tunnel verifies closing a stream does not break subsequent streams on that connection.
			for attempt := 0; attempt < 2; attempt++ {
				exerciseExtendedSocket(t, transport, version)
			}
		})
	}
}

// exerciseExtendedSocket holds both halves open until application frames and the close handshake complete.
func exerciseExtendedSocket(t *testing.T, transport http.RoundTripper, version int) {
	t.Helper()
	ctx, base := policyFixture(t)
	path := map[int]string{2: "/ws-h2", 3: "/ws-h3"}[version]
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	response, err := transport.RoundTrip(extendedRequest(t, ctx, base+path, version, "websocket", reader))
	// A successful extended CONNECT is HTTP 200, never the HTTP/1 101 Upgrade response.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	// This rules out transparent fallback to a different transport.
	if response.StatusCode != 200 || response.ProtoMajor != version {
		t.Fatalf("expected HTTP/%d 200; got %s %d", version, response.Proto, response.StatusCode)
	}
	// WebSocket text, binary and ping/pong must retain their framing across the proxy bridge.
	for _, frame := range []struct {
		sent, received byte
		payload        string
	}{{1, 1, "hostlier-extended-websocket"}, {2, 2, "\x00\x01\x02\xff"}, {9, 10, "ping"}, {8, 8, "\x03\xe8"}} {
		writeClientFrame(t, writer, frame.sent, []byte(frame.payload))
		readServerFrame(t, response.Body, frame.received, []byte(frame.payload))
	}
	t.Logf("HTTP/%d extended CONNECT: 200; text/binary echo, ping/pong and close passed", version)
}

// writeClientFrame sends bounded RFC 6455 frames with fresh client masking over the open request body.
func writeClientFrame(t *testing.T, writer io.Writer, opcode byte, payload []byte) {
	t.Helper()
	mask := make([]byte, 4)
	// Random masking is required even when frames travel inside HTTP/2 or HTTP/3 DATA.
	if _, err := rand.Read(mask); err != nil {
		t.Fatal(err)
	}
	frame := append([]byte{0x80 | opcode, 0x80 | byte(len(payload))}, mask...)
	// Fixture payloads are deliberately short so extended frame lengths are unnecessary.
	for index, value := range payload {
		frame = append(frame, value^mask[index%4])
	}
	// Failure to stream request bytes is a transport failure, not a successful handshake.
	if _, err := writer.Write(frame); err != nil {
		t.Fatal(err)
	}
}

// readServerFrame checks complete unmasked server frames, opcodes and exact binary payloads.
func readServerFrame(t *testing.T, reader io.Reader, opcode byte, payload []byte) {
	t.Helper()
	var header [2]byte
	// Each response must remain incremental while the request body is still open.
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		t.Fatal(err)
	}
	// The standard server role sends unmasked bounded frames with FIN set.
	if header[0] != 0x80|opcode || header[1] != byte(len(payload)) {
		t.Fatalf("unexpected frame header %x for opcode=%d length=%d", header, opcode, len(payload))
	}
	body := make([]byte, len(payload))
	// Partial frames cannot satisfy an echo or control-frame assertion.
	if _, err := io.ReadFull(reader, body); err != nil || string(body) != string(payload) {
		t.Fatalf("frame payload %x; want %x; error=%v", body, payload, err)
	}
}

// TestWebSocketExtendedDenials rejects wrong protocols, wrong versions and extended CONNECT on ordinary HTTP.
func TestWebSocketExtendedDenials(t *testing.T) {
	ctx, base := policyFixture(t)
	// Invalid protocol selectors must not become generic CONNECT tunnels.
	for _, version := range []int{2, 3} {
		transport := extendedTransport(t, version)
		path := map[int]string{2: "/ws-h2", 3: "/ws-h3"}[version]
		wrong := map[int]string{2: "/ws-h3", 3: "/ws-h2"}[version]
		// Includes case changes, descendants and a non-WebSocket HTTP resource.
		for _, item := range []struct{ path, protocol string }{{path, "not-websocket"}, {wrong, "websocket"}, {"/health", "websocket"}, {strings.ToUpper(path), "websocket"}, {path + "/child", "websocket"}} {
			response, err := transport.RoundTrip(extendedRequest(t, ctx, base+item.path, version, item.protocol, http.NoBody))
			// Rejection must come from the explicit edge policy, without HTTP/1 fallback.
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			// A 2xx CONNECT would grant a tunnel outside the reviewed permission.
			if response.StatusCode != 403 || response.ProtoMajor != version {
				t.Fatalf("HTTP/%d %s protocol=%s: %s %d", version, item.path, item.protocol, response.Proto, response.StatusCode)
			}
		}
	}
}
