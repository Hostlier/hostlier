package cloudsdk

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/controlrpc/controlrpcconnect"
)

// The fixture verifies the logical method, route and signed bytes in each RPC.
// The client still uses real gRPC/TLS framing and server streaming.
type sdkFixture struct {
	controlrpcconnect.UnimplementedControllerHandler
	api http.Handler
}

// Resume the same purge across both forms of disconnect observed on sslip.
func TestWatchResumesAfterCaddyReload(t *testing.T) {
	var watches atomic.Int32
	_, rpc := controlrpcconnect.NewControllerHandler(sdkFixture{api: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/email/session":
			_ = json.NewEncoder(w).Encode(map[string]string{"epoch": strings.Repeat("a", 64)})
		case "/v1/operations/test/events":
			_, _ = w.Write([]byte(`{"id":"test","action":"delete","purge":true,"status":"succeeded"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})})
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == rpcPrefix+"WatchOperation" {
			switch watches.Add(1) {
			case 1: // Close without a grpc-status trailer.
				w.Header().Set("Content-Type", "application/grpc")
				return
			case 2: // net/http sends RST_STREAM(INTERNAL_ERROR) on handler abort.
				w.Header().Set("Content-Type", "application/grpc")
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}
		}
		rpc.ServeHTTP(w, r)
	}))
	s.EnableHTTP2 = true
	s.StartTLS()
	defer s.Close()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := apiRunner{origin: s.URL, http: s.Client(), key: key, token: "test"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result struct{ Status string }
	if err := a.await(ctx, "operation", "test", operationRoute("test"), &result); err != nil || result.Status != "succeeded" || watches.Load() != 3 {
		t.Fatal(err, result, watches.Load())
	}
}

func newSDKServer(t *testing.T, api http.Handler) *httptest.Server {
	t.Helper()
	_, handler := controlrpcconnect.NewControllerHandler(sdkFixture{api: api})
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	return server
}

func (f sdkFixture) reply(method, path string, headers http.Header, body []byte) ([]byte, error) {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header = headers.Clone()
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	if w.Code != 200 {
		var result struct {
			Error struct{ Code, Message string }
		}
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		err := connect.NewError(connect.CodeFailedPrecondition, errors.New(result.Error.Message))
		err.Meta().Set("hostlier-error-code", result.Error.Code)
		return nil, err
	}
	return w.Body.Bytes(), nil
}
func (f sdkFixture) Command(ctx context.Context, in *connect.Request[pb.CommandRequest]) (*connect.Response[pb.CommandReply], error) {
	data, err := f.reply(in.Msg.Method, in.Msg.Route, in.Header(), in.Msg.JsonBody)
	return connect.NewResponse(&pb.CommandReply{JsonBody: data}), err
}
func (f sdkFixture) WatchOperation(ctx context.Context, in *connect.Request[pb.EventRequest], out *connect.ServerStream[pb.Event]) error {
	data, err := f.reply("GET", "/v1/operations/"+in.Msg.OperationId+"/events", in.Header(), nil)
	if err != nil {
		return err
	}
	return out.Send(&pb.Event{Kind: "operation", JsonData: data})
}
func (f sdkFixture) WatchJob(ctx context.Context, in *connect.Request[pb.JobRequest], out *connect.ServerStream[pb.Event]) error {
	data, err := f.reply("GET", in.Msg.Route, in.Header(), nil)
	if err != nil {
		return err
	}
	return out.Send(&pb.Event{Kind: "receipt", JsonData: data})
}
