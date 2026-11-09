package runtime

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"hostlier/internal/model"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// validateExtendedWebSocket verifies the selected Caddy bridge and real HTTP/2 SETTINGS before mutation.
func (r *Runtime) validateExtendedWebSocket(ctx context.Context, p model.Project) error {
	h2, h3 := false, false
	// Only explicit WebSocket version requirements impose the new capability gate.
	for _, endpoint := range model.EndpointProjects(p) {
		a, b := model.ExtendedWebSocketVersions(endpoint.Endpoint)
		h2, h3 = h2 || a, h3 || b
	}
	// Existing HTTP/1 WebSocket routes keep their previous compatibility contract.
	if !h2 && !h3 {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	executable := model.CaddyBinaryPath
	if _, err := os.Stat(executable); os.IsNotExist(err) {
		executable = "caddy"
	}
	version, err := exec.CommandContext(bounded, executable, "version").Output()
	// Reject older bridges rather than accepting a policy that cannot proxy its handshake.
	if err != nil || !extendedCaddyVersion(string(version)) {
		return model.Err("EDGE_WEBSOCKET_UNSUPPORTED", "WebSocket extended CONNECT requires Caddy 2.11.7 or newer")
	}
	// HTTP/3 advertises extended CONNECT natively; HTTP/2 additionally needs its server setting.
	if h2 {
		return r.probeHTTP2Connect(bounded)
	}
	return nil
}

// extendedCaddyVersion bounds support to versions with the tested HTTP/2-and-HTTP/3 upgrade bridge.
func extendedCaddyVersion(value string) bool {
	var major, minor, patch int
	_, err := fmt.Sscanf(strings.TrimSpace(value), "v%d.%d.%d", &major, &minor, &patch)
	return err == nil && (major > 2 || major == 2 && (minor > 11 || minor == 11 && patch >= 7))
}

// probeHTTP2Connect checks SETTINGS_ENABLE_CONNECT_PROTOCOL without sending any application request.
func (r *Runtime) probeHTTP2Connect(ctx context.Context) error {
	dialer := &tls.Dialer{Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: r.options.Hostname, NextProtos: []string{"h2"}}}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(r.options.ProbeHost, "443"))
	// Capability checks preserve normal certificate verification and never fall back to plaintext.
	if err != nil {
		return model.Err("EDGE_WEBSOCKET_UNSUPPORTED", "Cannot verify the owned TLS edge's HTTP/2 CONNECT capability")
	}
	defer connection.Close()
	deadline, _ := ctx.Deadline()
	_ = connection.SetDeadline(deadline)
	// A client preface plus empty SETTINGS starts negotiation without invoking a route.
	if _, err := io.WriteString(connection, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00"); err != nil {
		return err
	}
	return readConnectSetting(connection)
}

// readConnectSetting accepts only the server's bounded initial SETTINGS frame and the standardized opt-in.
func readConnectSetting(reader io.Reader) error {
	var header [9]byte
	// The first server frame must describe connection-level settings.
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return err
	}
	length := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
	// Invalid framing cannot be mistaken for a supported capability.
	if !validConnectSettingsHeader(header, length) {
		return model.Err("EDGE_WEBSOCKET_UNSUPPORTED", "Invalid HTTP/2 edge SETTINGS response")
	}
	settings := make([]byte, length)
	// Read the complete settings payload before inspecting individual values.
	if _, err := io.ReadFull(reader, settings); err != nil {
		return err
	}
	// Setting 0x8 is ENABLE_CONNECT_PROTOCOL, defined by RFC 8441.
	for offset := 0; offset < length; offset += 6 {
		// Only an explicit value of one enables extended CONNECT.
		if binary.BigEndian.Uint16(settings[offset:]) == 8 && binary.BigEndian.Uint32(settings[offset+2:]) == 1 {
			return nil
		}
	}
	return model.Err("EDGE_WEBSOCKET_UNSUPPORTED", "Caddy HTTP/2 extended CONNECT is disabled; set GODEBUG=http2xconnect=1 on caddy.service and restart Caddy")
}

// validConnectSettingsHeader bounds framing before allocating or decoding server settings.
func validConnectSettingsHeader(header [9]byte, length int) bool {
	return header[3] == 4 && header[4] == 0 && binary.BigEndian.Uint32(header[5:]) == 0 && length <= 16384 && length%6 == 0
}
