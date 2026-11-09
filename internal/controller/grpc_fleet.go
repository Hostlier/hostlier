package controller

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"time"

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
	peer, config, actor, err := s.watchPeer(r, host)
	if err != nil {
		return watchRPCError(err)
	}
	if !s.c.observations.reserveStatusStream() {
		return publicRPCError(model.Err("RATE_LIMITED", "Watch capacity exceeded"))
	}
	defer s.c.observations.releaseStatusStream()
	transport := &http.Transport{TLSClientConfig: config, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	api := controlrpcconnect.NewControllerClient(&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, peer.URL, connect.WithGRPC(), connect.WithReadMaxBytes(4<<20+1024))
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
		return err
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
	return upstream.Err()
}

func (s controlService) watchPeer(r *http.Request, name string) (HostRecord, *tls.Config, string, error) {
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
	identity, err := c.fleetIdentity()
	if err != nil {
		return host, nil, "", err
	}
	config, err := fleet.TLS(identity, host.Fingerprint, false)
	return host, config, strings.TrimPrefix(c.store.Authenticate("fleet-actor|"+actor), "hmac:"), err
}
