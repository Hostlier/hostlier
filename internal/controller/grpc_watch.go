package controller

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/model"
)

// WatchOperation replays durable frames, then pushes changes. A stream owns
// only its reservation; cancellation never cancels or resubmits the job.
func (s controlService) WatchOperation(ctx context.Context, in *connect.Request[pb.EventRequest], out *connect.ServerStream[pb.Event]) error {
	id := in.Msg.OperationId
	if id == "" || len(id) > 200 || strings.ContainsAny(id, "/:\x00\r\n") {
		return publicRPCError(model.Err("INVALID_REQUEST", "Invalid operation identity"))
	}
	route := "/v1/operations/" + url.PathEscape(id) + "/events"
	if in.Msg.Host != "" {
		return s.forwardWatch(ctx, in.Msg.Host, route, in.Msg, nil, out)
	}
	r, err := logicalRequest(ctx, "GET", route, nil)
	if err != nil {
		return watchRPCError(err)
	}
	r.Header.Set("Last-Event-ID", in.Msg.Cursor)
	after, err := s.c.acquireEventStream(r, s.local, id)
	if err != nil {
		return watchRPCError(err)
	}
	defer s.c.releaseEventStream()
	send := streamSender(ctx, out)
	heartbeat := time.NewTicker(eventStreamHeartbeat)
	defer heartbeat.Stop()
	status := ""
	for {
		snap, err := s.c.snapshotEvents(r, s.local, id, after, status)
		if err != nil {
			return watchRPCError(err)
		}
		for _, f := range snap.frames {
			if err := send(&pb.Event{Kind: f.kind, Cursor: f.id, JsonData: f.data}); err != nil {
				return err
			}
		}
		if snap.terminal {
			return nil
		}
		after, status = snap.after, snap.status
		if err := s.waitChange(r, snap.changed, nil, heartbeat.C, send); err != nil {
			return err
		}
	}
}

// A heartbeat rechecks authority but never fetches progress. Receipt expiry
// can wake a job even if no other controller state has changed.
func (s controlService) waitChange(r *http.Request, changed <-chan struct{}, expiry, heartbeat <-chan time.Time, send func(*pb.Event) error) error {
	for {
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-s.c.ctx.Done():
			return connect.NewError(connect.CodeUnavailable, model.Err("UNAVAILABLE", "Controller stopped"))
		case <-changed:
			return nil
		case <-expiry:
			return nil
		case <-heartbeat:
			if err := s.c.statusAuthority(r, s.local); err != nil {
				return watchRPCError(err)
			}
			if err := send(&pb.Event{Kind: "heartbeat"}); err != nil {
				return err
			}
		}
	}
}

// WatchStatus subscribes to the shared capture bus, sending a full replacement
// on every new connection and checking live authority before each disclosure.
func (s controlService) WatchStatus(ctx context.Context, _ *connect.Request[pb.StatusRequest], out *connect.ServerStream[pb.Event]) error {
	r, err := logicalRequest(ctx, "GET", "/v1/status/events", nil)
	if err != nil {
		return watchRPCError(err)
	}
	if err := s.c.statusAuthority(r, s.local); err != nil {
		return watchRPCError(err)
	}
	capture := s.c.observations
	if !capture.reserveStatusStream() {
		return publicRPCError(model.Err("RATE_LIMITED", "Status stream capacity exceeded"))
	}
	defer capture.releaseStatusStream()
	updates, unsubscribe := capture.bus.Subscribe()
	defer unsubscribe()
	send := streamSender(ctx, out)
	heartbeat := time.NewTicker(eventStreamHeartbeat)
	defer heartbeat.Stop()
	snapshot := true
	for {
		if err := s.c.statusAuthority(r, s.local); err != nil {
			return watchRPCError(err)
		}
		capture.touch()
		frame := &pb.Event{Kind: "heartbeat"}
		if snapshot {
			frame = &pb.Event{Kind: "snapshot", JsonData: []byte(s.c.latestStatus())}
		}
		if err := send(frame); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.c.ctx.Done():
			return connect.NewError(connect.CodeUnavailable, model.Err("UNAVAILABLE", "Controller stopped"))
		case <-updates:
			snapshot = true
		case <-heartbeat.C:
			snapshot = false
		}
	}
}

// Bound each send independently so an idle healthy stream stays open, while
// a stalled peer cannot indefinitely occupy a subscription slot.
func streamSender(ctx context.Context, stream *connect.ServerStream[pb.Event]) func(*pb.Event) error {
	control := http.NewResponseController(ctx.Value(rpcTransportKey{}).(rpcTransport).writer)
	return func(frame *pb.Event) error {
		if err := control.SetWriteDeadline(time.Now().Add(eventStreamWriteTimeout)); err != nil {
			return err
		}
		if err := stream.Send(frame); err != nil {
			return err
		}
		return control.SetWriteDeadline(time.Time{})
	}
}
