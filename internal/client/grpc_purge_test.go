package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/controlrpc/controlrpcconnect"
)

func TestPurgeWatchResumesAfterProxyReset(t *testing.T) {
	const id = "purge-1"
	path := controlrpcconnect.ControllerWatchOperationProcedure
	var calls atomic.Int32
	handler := connect.NewServerStreamHandler(path, func(ctx context.Context, r *connect.Request[pb.EventRequest], out *connect.ServerStream[pb.Event]) error {
		if r.Msg.OperationId != id {
			t.Errorf("watched wrong operation: %q", r.Msg.OperationId)
		}
		if r.Msg.Cursor == "" {
			return out.Send(&pb.Event{Kind: "log", Cursor: id + ":7", JsonData: []byte(`{"operationId":"purge-1","sequence":7}`)})
		}
		if r.Msg.Cursor != id+":7" {
			t.Errorf("unexpected resume cursor: %q", r.Msg.Cursor)
		}
		return out.Send(&pb.Event{Kind: "operation", JsonData: []byte(`{"id":"purge-1","status":"succeeded","action":"delete","purge":true}`)})
	})
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			t.Errorf("unexpected RPC %s", r.URL.Path)
		}
		if calls.Add(1) == 2 {
			w.Header().Set("Content-Type", "application/grpc")
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		handler.ServeHTTP(w, r)
	}))
	s.EnableHTTP2 = true
	s.StartTLS()
	defer s.Close()
	api := &Client{URL: s.URL, HTTP: s.Client(), grpc: true}
	var events []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := api.WatchOperation(ctx, id, "", func(e OperationEvent) error { events = append(events, e.Event); return nil })
	if err != nil || calls.Load() != 3 || !slices.Equal(events, []string{"log", "operation"}) {
		t.Fatal(err, calls.Load(), events)
	}
}

func TestWireInternalErrorDoesNotRetryWatch(t *testing.T) {
	err := connect.NewWireError(connect.CodeInternal, errors.New("controller failure"))
	if streamObservationError(streamEnd(context.Background(), err)) == nil {
		t.Fatal("treated a terminal server status as a lost stream")
	}
}
