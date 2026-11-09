package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/controlrpc/controlrpcconnect"
	"hostlier/internal/model"
)

// NewGRPC selects native gRPC for CLI connections, retaining the same origin,
// trust roots, device credential and owner-only Unix socket as the HTTP API.
func NewGRPC(options Options) (*Client, error) {
	c, err := New(options)
	if err != nil {
		return nil, err
	}
	t := c.HTTP.Transport.(*http.Transport)
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: 30 * time.Second, PingTimeout: 10 * time.Second}
	t.Protocols = new(http.Protocols)
	if options.SocketPath != "" {
		t.Protocols.SetUnencryptedHTTP2(true)
	} else {
		t.Protocols.SetHTTP2(true)
	}
	c.grpc = true
	return c, nil
}

func (c *Client) service(transport *http.Client, limit int) controlrpcconnect.ControllerClient {
	// The protobuf field tag and length prefix are outside the JSON size limit.
	return controlrpcconnect.NewControllerClient(transport, c.URL, connect.WithGRPC(), connect.WithReadMaxBytes(limit+1024))
}

func (c *Client) rpcCommand(r *http.Request, data []byte, result any, watching bool) error {
	path := r.URL.RequestURI()
	limit := responseLimit(path)
	call := connect.NewRequest(&pb.CommandRequest{Method: r.Method, Route: path, JsonBody: data})
	copyAuthority(call.Header(), r.Header)
	response, err := c.service(c.requestTransport(r.Method, path), int(limit)).Command(r.Context(), call)
	if err != nil {
		return rpcError(r.Context(), err, watching)
	}
	raw := response.Msg.JsonBody
	if int64(len(raw)) > limit {
		return model.Err("RESPONSE_LIMIT", "Controller response too large")
	}
	if result != nil && len(raw) > 0 && json.Unmarshal(raw, result) != nil {
		return model.Err("RESPONSE", "Invalid controller JSON response")
	}
	return nil
}

// Only identity metadata accompanies an RPC; content type, host and framing
// belong to the actual gRPC transport.
func copyAuthority(dst, src http.Header) {
	for _, name := range []string{"Authorization", "X-Hostlier-Epoch", "X-Hostlier-Time", "X-Hostlier-Nonce", "X-Hostlier-Signature"} {
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
}

// Keep controller failures (including authorization and stale approval) distinct
// from drops. Never use raw intermediary diagnostics as operator output.
func rpcError(ctx context.Context, err error, watching bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		for _, detail := range ce.Details() {
			msg, _ := detail.Value()
			if e, ok := msg.(*pb.ControllerError); ok && e.Code != "" {
				return model.Err(e.Code, ce.Message())
			}
		}
		switch ce.Code() {
		case connect.CodeUnauthenticated:
			return model.Err("UNAUTHORIZED", "Controller authentication required")
		case connect.CodePermissionDenied:
			return model.Err("SCOPE_DENIED", "Controller access denied")
		case connect.CodeResourceExhausted:
			return model.Err("RESPONSE_LIMIT", "Controller message exceeds limit")
		case connect.CodeUnimplemented:
			return model.Err("GRPC_UNSUPPORTED", "Controller gRPC unavailable; upgrade the controller and its proxy configuration")
		}
	}
	if invalidCertificate(err) {
		return model.Err("TLS", "Cannot verify controller HTTPS certificate")
	}
	if watching {
		if pb.IsTransportInterruption(err) || connect.CodeOf(err) == connect.CodeUnavailable {
			return io.EOF
		}
		return streamConnectionError(ctx, err)
	}
	return model.Err("CONNECTION", "Cannot reach controller over gRPC; verify its certificate, address, and HTTP/2 proxy configuration")
}

// A healthy subscription has no wall-clock timeout. TLS handshakes, caller
// cancellation, and the controller's per-send deadline still apply.
func (c *Client) watchService() controlrpcconnect.ControllerClient {
	transport := *c.HTTP
	transport.Timeout = 0
	return c.service(&transport, MaxOperationFrameBytes)
}

func (c *Client) signedWatch(ctx context.Context, path string, header http.Header) error {
	r, err := http.NewRequestWithContext(ctx, "GET", c.URL+path, nil)
	if err != nil {
		return err
	}
	if err = c.authorize(r, nil, true); err != nil {
		return err
	}
	copyAuthority(header, r.Header)
	return nil
}

// streamEnd distinguishes ordinary rotation and interrupted transport from a
// permanent protocol error. Clean EOF without a terminal operation resumes.
func streamEnd(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return io.EOF
	}
	return rpcError(ctx, err, true)
}
