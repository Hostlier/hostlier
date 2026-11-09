package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"hostlier/internal/client"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/controlrpc/controlrpcconnect"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// planningReconnectFixture drops acceptance and observation replies after durable acceptance.
type planningReconnectFixture struct {
	mu            sync.Mutex
	requestID     string
	posts         atomic.Int32
	subscriptions atomic.Int32
	applies       atomic.Int32
	completed     atomic.Bool
	invalid       atomic.Bool
}

// Command drops the first accepted reply. Any unary progress read is a failure:
// only a subscription may deliver the prepared plan.
func (f *planningReconnectFixture) command(_ context.Context, in *connect.Request[pb.CommandRequest]) (*connect.Response[pb.CommandReply], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var reply any
	switch in.Msg.Method + " " + in.Msg.Route {
	case "POST /v1/planning":
		var body struct{ RequestID string }
		if json.Unmarshal(in.Msg.JsonBody, &body) != nil || body.RequestID == "" {
			f.invalid.Store(true)
		}
		if f.posts.Add(1) == 1 {
			f.requestID = body.RequestID
			return nil, connect.NewError(connect.CodeUnavailable, io.EOF)
		}
		if f.requestID != body.RequestID {
			f.invalid.Store(true)
		}
		reply = map[string]string{"id": "job", "status": "queued"}
	case "POST /v1/approvals":
		var request struct {
			Path string
			Body map[string]any
		}
		if json.Unmarshal(in.Msg.JsonBody, &request) != nil || request.Path != "/v1/plans/review/apply" || request.Body["fingerprint"] != "exact-review" || request.Body["approved"] != true || !f.completed.Load() {
			f.invalid.Store(true)
		}
		reply = map[string]string{"id": "fixture", "status": "approved"}
	case "POST /v1/approvals/fixture/execute":
		f.applies.Add(1)
		reply = map[string]string{"operationId": "deployment"}
	default:
		f.invalid.Store(true)
		return nil, connect.NewError(connect.CodeUnimplemented, io.EOF)
	}
	data, err := json.Marshal(reply)
	return connect.NewResponse(&pb.CommandReply{JsonBody: data}), err
}

// TestWorkflowPlanningReconnectRequiresExactApproval exercises the real HTTPS client and CLI review.
func TestWorkflowPlanningReconnectRequiresExactApproval(t *testing.T) {
	fixture := &planningReconnectFixture{}
	mux := http.NewServeMux()
	mux.Handle(controlrpcconnect.ControllerWatchJobProcedure, connect.NewServerStreamHandler(controlrpcconnect.ControllerWatchJobProcedure, func(ctx context.Context, in *connect.Request[pb.JobRequest], stream *connect.ServerStream[pb.Event]) error {
		if in.Msg.Route != "/v1/planning/job" {
			fixture.invalid.Store(true)
		}
		if fixture.subscriptions.Add(1) == 1 {
			return connect.NewError(connect.CodeUnavailable, io.EOF)
		}
		if err := stream.Send(&pb.Event{Kind: "receipt", JsonData: []byte(`{"id":"job","status":"running"}`)}); err != nil {
			return err
		}
		fixture.completed.Store(true)
		return stream.Send(&pb.Event{Kind: "receipt", JsonData: []byte(`{"id":"job","status":"completed","plan":{"id":"review","fingerprint":"exact-review"}}`)})
	}))
	mux.Handle(controlrpcconnect.ControllerCommandProcedure, connect.NewUnaryHandler(controlrpcconnect.ControllerCommandProcedure, fixture.command))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	remote, err := client.NewGRPC(client.Options{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	remote.HTTP = server.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	approvals := 0
	// Exercise each controlled fixture path without real provider mutations.
	s := &session{ctx: ctx, remote: remote, out: &stdout, errOut: &stderr, interactive: true, flags: map[string]string{}, options: Options{PollInterval: time.Millisecond, Prompt: func(string, bool) (string, error) {
		approvals++
		// Verify the expected preparation and approval boundary.
		if !fixture.completed.Load() {
			t.Error("approval preceded completed review")
		}
		return "yes", nil
	}}}
	flow := planFlow{path: "/v1/plans", body: map[string]any{"spec": model.Project{Workflow: &workflow.Spec{}}}, prompt: "Approve?"}
	receipt, err := s.reviewPlanAttempt(flow)
	// Verify the expected preparation and approval boundary.
	if err != nil || receipt.OperationID != "deployment" {
		t.Fatal(receipt, err)
	}
	// Verify the expected preparation and approval boundary.
	if fixture.posts.Load() != 2 || fixture.subscriptions.Load() != 2 || fixture.applies.Load() != 1 || approvals != 1 || fixture.invalid.Load() {
		t.Fatal("reconnect changed planning or approval identity")
	}
	// Verify the expected preparation and approval boundary.
	if !strings.Contains(stderr.String(), "no deployment approved") {
		t.Fatal("preparation progress missing")
	}
}

// TestNonworkflowPlansRemainSynchronous preserves compatibility with existing controller endpoints.
func TestNonworkflowPlansRemainSynchronous(t *testing.T) {
	// Verify the expected preparation and approval boundary.
	if asynchronousPlan(planFlow{path: "/v1/plans", body: map[string]any{"spec": model.Project{}}}) {
		t.Fatal("legacy plan became asynchronous")
	}
	// Verify the expected preparation and approval boundary.
	if asynchronousPlan(planFlow{path: "/v1/resource-plans", body: map[string]any{"spec": model.Project{Workflow: &workflow.Spec{}}}}) {
		t.Fatal("resource route changed")
	}
}
