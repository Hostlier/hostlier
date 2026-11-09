package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/controlrpc/controlrpcconnect"
	"hostlier/internal/model"
)

// The listener chooses local authority; RPC metadata can never select it.
type controlService struct {
	c     *Controller
	local bool
}

type rpcTransport struct {
	request *http.Request
	writer  http.ResponseWriter
}
type rpcTransportKey struct{}

func (c *Controller) managementHandler(local bool) http.Handler {
	path, handler := controlrpcconnect.NewControllerHandler(controlService{c, local}, connect.WithReadMaxBytes(maxRequestBytes+16384))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, path) || (!local && c.tunnelHost(r.Host)) {
			c.handle(w, r, local)
			return
		}
		// Keep browser, tunnel and HTTP integration routes separate from native gRPC.
		media, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
		if r.ProtoMajor != 2 || (media != "application/grpc" && media != "application/grpc+proto") {
			http.Error(w, "Native gRPC over HTTP/2 required", http.StatusUnsupportedMediaType)
			return
		}
		ctx := context.WithValue(r.Context(), rpcTransportKey{}, rpcTransport{r, w})
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

// logicalRequest binds the existing authorization and dispatch rules to the exact
// signed method, route and body while retaining the actual peer and TLS identity.
func logicalRequest(ctx context.Context, method, route string, data []byte) (*http.Request, error) {
	u, err := url.ParseRequestURI(route)
	if err != nil || u.IsAbs() || !(u.Path == "/health" || strings.HasPrefix(u.Path, "/v1/")) {
		return nil, model.Err("API_PATH", "Invalid controller route")
	}
	switch method {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD":
	default:
		return nil, model.Err("INVALID_REQUEST", "Invalid controller method")
	}
	wire := ctx.Value(rpcTransportKey{}).(rpcTransport)
	r := wire.request.Clone(ctx)
	r.Method, r.URL, r.RequestURI = method, u, route
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	r.Header.Set("Content-Type", "application/json")
	return r, nil
}

// Command reuses the same bounded, authenticated management dispatcher as the
// HTTP API. It never dials a second listener or forwards a client-supplied URL.
func (s controlService) Command(ctx context.Context, in *connect.Request[pb.CommandRequest]) (*connect.Response[pb.CommandReply], error) {
	m := in.Msg
	r, err := logicalRequest(ctx, m.Method, m.Route, m.JsonBody)
	if err != nil {
		return nil, publicRPCError(err)
	}
	if int64(len(m.JsonBody)) > apiBodyLimit(r, s.local) {
		return nil, publicRPCError(model.Err("INVALID_REQUEST", "Controller request too large"))
	}
	if operationEventsRoute(r) || r.URL.Path == "/v1/status/events" {
		return nil, publicRPCError(model.Err("INVALID_REQUEST", "Use the corresponding streaming RPC"))
	}
	w := &commandWriter{header: make(http.Header), transport: ctx.Value(rpcTransportKey{}).(rpcTransport).writer}
	s.c.handleManagement(w, r, s.local)
	if w.status >= 300 {
		var envelope struct{ Error *model.Error }
		_ = json.Unmarshal(w.Bytes(), &envelope)
		if envelope.Error == nil {
			return nil, publicRPCError(errors.New("controller error"))
		}
		return nil, publicRPCError(envelope.Error)
	}
	return connect.NewResponse(&pb.CommandReply{JsonBody: w.Bytes()}), nil
}

// Only the JSON payload goes in the protobuf reply. Deadlines still apply to
// the underlying stream (including the longer approved-execution timeout).
type commandWriter struct {
	bytes.Buffer
	header    http.Header
	status    int
	transport http.ResponseWriter
}

func (w *commandWriter) Header() http.Header  { return w.header }
func (w *commandWriter) WriteHeader(code int) { w.status = code }
func (w *commandWriter) SetWriteDeadline(t time.Time) error {
	return http.NewResponseController(w.transport).SetWriteDeadline(t)
}

func publicRPCError(err error) error {
	var e *model.Error
	if !errors.As(err, &e) {
		return connect.NewError(connect.CodeInternal, errors.New("Controller request failed"))
	}
	code := map[int]connect.Code{400: connect.CodeInvalidArgument, 401: connect.CodeUnauthenticated, 403: connect.CodePermissionDenied, 404: connect.CodeNotFound, 409: connect.CodeFailedPrecondition, 429: connect.CodeResourceExhausted, 503: connect.CodeUnavailable}[errorStatus(e.Code)]
	out := connect.NewError(code, errors.New(e.Message))
	out.Meta().Set("hostlier-error-code", e.Code)
	detail, err := connect.NewErrorDetail(&pb.ControllerError{Code: e.Code})
	if err == nil {
		out.AddDetail(detail)
	}
	return out
}

// A gracefully stopping controller is an interrupted subscription, not a final
// job failure. Let clients reopen the same watch once the controller returns.
func watchRPCError(err error) error {
	var failure *model.Error
	if errors.As(err, &failure) && failure.Code == "UNAVAILABLE" {
		return connect.NewError(connect.CodeUnavailable, errors.New(failure.Message))
	}
	return publicRPCError(err)
}
