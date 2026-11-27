package controller

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/controlrpc/controlrpcconnect"
	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// Only the pinned master may subscribe on an agent. Command and administrative
// watches are never exposed by the agent listener.
func agentWatch(path string) bool {
	return path == controlrpcconnect.ControllerWatchOperationProcedure || path == controlrpcconnect.ControllerWatchJobProcedure
}

func (s controlService) forwardWatch(ctx context.Context, host, route string, op *pb.EventRequest, job *pb.JobRequest, out *connect.ServerStream[pb.Event]) error {
	if !fleet.ValidHostName(host) || !model.FleetRoute(route) || ctx.Value(fleetContext{}) != nil {
		return publicRPCError(model.Err("INVALID_HOST", "Invalid host subscription"))
	}
	r, err := logicalRequest(ctx, "GET", "/v1/hosts/"+host+"/proxy/"+strings.TrimPrefix(route, "/v1/"), nil)
	if err != nil {
		return watchRPCError(err)
	}
	peer, transport, actor, err := s.watchPeer(r, host)
	if err != nil {
		return watchRPCError(err)
	}
	if !s.c.observations.reserveStatusStream() {
		return publicRPCError(model.Err("RATE_LIMITED", "Watch capacity exceeded"))
	}
	defer s.c.observations.releaseStatusStream()
	api := controlrpcconnect.NewControllerClient(&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, "https://fleet.invalid", connect.WithGRPC(), connect.WithReadMaxBytes(4<<20+1024))
	var upstream *connect.ServerStreamForClient[pb.Event]
	if op != nil {
		call := connect.NewRequest(&pb.EventRequest{OperationId: op.OperationId, Cursor: op.Cursor})
		call.Header().Set("X-Hostlier-Actor", actor)
		upstream, err = api.WatchOperation(ctx, call)
	} else {
		call := connect.NewRequest(&pb.JobRequest{Route: job.Route})
		call.Header().Set("X-Hostlier-Actor", actor)
		upstream, err = api.WatchJob(ctx, call)
	}
	if err != nil {
		return fleetWatchError(ctx, transport, err)
	}
	defer upstream.Close()
	send := streamSender(ctx, out)
	for upstream.Receive() {
		// A peer switch or credential revocation cannot leak another update.
		s.c.mu.Lock()
		_, err := s.c.authenticate(r, s.local, false)
		current := s.c.state.Hosts[host]
		s.c.mu.Unlock()
		if err != nil {
			return watchRPCError(err)
		}
		if current.Fingerprint != peer.Fingerprint || current.URL != peer.URL {
			return publicRPCError(model.Err("HOST_REVOKED", "Host enrollment changed"))
		}
		if err := send(upstream.Msg()); err != nil {
			return err
		}
	}
	return fleetWatchError(ctx, transport, upstream.Err())
}

func (s controlService) watchPeer(r *http.Request, name string) (HostRecord, http.RoundTripper, string, error) {
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	actor, err := c.authenticate(r, s.local, false)
	if err != nil {
		return HostRecord{}, nil, "", err
	}
	host, ok := c.state.Hosts[name]
	if !ok {
		return HostRecord{}, nil, "", model.Err("NOT_FOUND", "Enrolled host not found")
	}
	transport, err := c.fleetTransport(host)
	return host, transport, strings.TrimPrefix(c.store.Authenticate("fleet-actor|"+actor), "hmac:"), err
}

// A lost upstream transport becomes an explicit retryable gRPC status at the
// master boundary; forwarding its raw error would turn it into a wire failure.
func fleetWatchError(ctx context.Context, transport http.RoundTripper, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	connection, ok := transport.(interface{ CanTakeNewRequest() bool })
	if err != nil && (pb.IsTransportInterruption(err) || ok && !connection.CanTakeNewRequest()) {
		return connect.NewError(connect.CodeUnavailable, model.Err("HOST_UNAVAILABLE", "Member controller disconnected"))
	}
	return err
}
