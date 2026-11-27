package fleetjoin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/controlrpc/controlrpcconnect"
	"hostlier/internal/model"
)

// transport has no user credentials or profile store: it serves local administration and public joins only.
type transport struct {
	http *http.Client
	api  controlrpcconnect.ControllerClient
}

// newTransport uses native gRPC with verified HTTPS or the explicitly selected protected socket.
func newTransport(origin, socket, caFile string) (*transport, error) {
	t, base, err := pb.NewHTTPTransport(origin, socket, caFile)
	if err != nil {
		return nil, err
	}
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: 30 * time.Second, PingTimeout: 10 * time.Second}
	t.Protocols = new(http.Protocols)
	if socket != "" {
		t.Protocols.SetUnencryptedHTTP2(true)
	} else {
		t.Protocols.SetHTTP2(true)
	}
	httpClient := &http.Client{Transport: t, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &transport{http: httpClient, api: controlrpcconnect.NewControllerClient(httpClient, base, connect.WithGRPC(), connect.WithReadMaxBytes(65536+1024))}, nil
}

// Request bounds enrollment responses and preserves structured errors without intermediary diagnostics.
func (t *transport) Request(ctx context.Context, method, path string, body, result any) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	response, err := t.api.Command(ctx, connect.NewRequest(&pb.CommandRequest{Method: method, Route: path, JsonBody: raw}))
	if err != nil {
		return transportError(ctx, err)
	}
	if len(response.Msg.JsonBody) > 65536 {
		return model.Err("RESPONSE_LIMIT", "Controller join response too large")
	}
	if result != nil && json.Unmarshal(response.Msg.JsonBody, result) != nil {
		return model.Err("JOIN_PROTOCOL", "Invalid controller join response")
	}
	return nil
}

// transportError distinguishes permanent controller rejections from safely retryable observations.
func transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *connect.Error
	if errors.As(err, &failure) {
		for _, detail := range failure.Details() {
			message, _ := detail.Value()
			if e, ok := message.(*pb.ControllerError); ok && e.Code != "" {
				return model.Err(e.Code, failure.Message())
			}
		}
		if failure.Code() == connect.CodeUnimplemented {
			return model.Err("GRPC_UNSUPPORTED", "Controller gRPC unavailable; upgrade the controller and proxy configuration")
		}
		if failure.Code() == connect.CodePermissionDenied || failure.Code() == connect.CodeUnauthenticated {
			return model.Err("UNAUTHORIZED", "Controller access denied")
		}
	}
	return model.Err("CONNECTION", "Cannot reach controller over gRPC; verify its certificate, address, and HTTP/2 proxy configuration")
}
