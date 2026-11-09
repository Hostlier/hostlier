package cloudsdk

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
)

const rpcPrefix = "/hostlier.control.v1.Controller/"

func (a *apiRunner) command(ctx context.Context, r *http.Request, data []byte, output any) error {
	call := connect.NewRequest(&pb.CommandRequest{Method: r.Method, Route: r.URL.RequestURI(), JsonBody: data})
	for name, values := range r.Header {
		call.Header()[name] = values
	}
	api := connect.NewClient[pb.CommandRequest, pb.CommandReply](a.http, a.origin+rpcPrefix+"Command", connect.WithGRPC(), connect.WithReadMaxBytes(4*maxDocument+1024))
	reply, err := api.CallUnary(ctx, call)
	if err != nil {
		return sdkRPCError(ctx, err)
	}
	if len(reply.Msg.JsonBody) > 4*maxDocument {
		return errors.New("controller response too large")
	}
	if output != nil {
		return json.Unmarshal(reply.Msg.JsonBody, output)
	}
	return nil
}

func sdkRPCError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var rpc *connect.Error
	if errors.As(err, &rpc) {
		if code := rpc.Meta().Get("hostlier-error-code"); code != "" {
			return &apiFailure{Code: code, Message: rpc.Message()}
		}
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return &apiFailure{Code: "TLS", Message: "Cannot verify controller HTTPS certificate", Cause: err}
	}
	code := "CONTROLLER"
	if connect.CodeOf(err) == connect.CodeUnavailable || pb.IsTransportInterruption(err) {
		code = "CONNECTION"
	}
	return &apiFailure{Code: code, Message: "Hostlier controller gRPC request failed", Cause: err}
}

// Only these two stream methods are needed by the SDK. Reconnecting observes
// the original receipt; it never repeats a deployment or approval.
func (a *apiRunner) await(ctx context.Context, kind, id, path string, output any) error {
	var cursor string
	for {
		terminal, err := a.subscribe(ctx, kind, id, path, &cursor)
		if err == nil && terminal != nil {
			return json.Unmarshal(terminal, output)
		}
		var failure *apiFailure
		if err != nil && (!errors.As(err, &failure) || failure.Code != "CONNECTION") {
			return err
		}
		if err := sdkPause(ctx); err != nil {
			return err
		}
	}
}

func (a *apiRunner) subscribe(ctx context.Context, kind, id, path string, cursor *string) ([]byte, error) {
	logical, err := http.NewRequestWithContext(ctx, "GET", a.origin+path, nil)
	if err != nil {
		return nil, err
	}
	if err = a.sign(logical, nil); err != nil {
		return nil, err
	}
	transport := *a.http
	transport.Timeout = 0
	var stream *connect.ServerStreamForClient[pb.Event]
	if kind == "operation" {
		call := connect.NewRequest(&pb.EventRequest{OperationId: id, Cursor: *cursor})
		for k, v := range logical.Header {
			call.Header()[k] = v
		}
		api := connect.NewClient[pb.EventRequest, pb.Event](&transport, a.origin+rpcPrefix+"WatchOperation", connect.WithGRPC(), connect.WithReadMaxBytes(4*maxDocument+1024))
		stream, err = api.CallServerStream(ctx, call)
	} else {
		call := connect.NewRequest(&pb.JobRequest{Route: path})
		for k, v := range logical.Header {
			call.Header()[k] = v
		}
		api := connect.NewClient[pb.JobRequest, pb.Event](&transport, a.origin+rpcPrefix+"WatchJob", connect.WithGRPC(), connect.WithReadMaxBytes(4*maxDocument+1024))
		stream, err = api.CallServerStream(ctx, call)
	}
	if err != nil {
		return nil, sdkRPCError(ctx, err)
	}
	defer stream.Close()
	for stream.Receive() {
		event := stream.Msg()
		switch event.Kind {
		case "heartbeat", "reset":
			continue
		case "log":
			if kind != "operation" || !strings.HasPrefix(event.Cursor, id+":") {
				return nil, errors.New("invalid log cursor")
			}
			*cursor = event.Cursor
		default:
			var state struct{ ID, Status string }
			if event.Kind != kind || json.Unmarshal(event.JsonData, &state) != nil || state.ID != id || state.Status == "" {
				return nil, errors.New("invalid controller watch event")
			}
			switch state.Status {
			case "queued", "running", "pending", "accepted":
			default:
				return event.JsonData, nil
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, sdkRPCError(ctx, err)
	}
	return nil, nil
}

func operationRoute(id string) string { return "/v1/operations/" + url.PathEscape(id) + "/events" }
