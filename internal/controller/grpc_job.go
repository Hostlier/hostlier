package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/model"
)

// WatchJob publishes replacement receipts for the same actor-bound read as the
// one-shot API. It does not poll a worker or repeat the initiating mutation.
func (s controlService) WatchJob(ctx context.Context, in *connect.Request[pb.JobRequest], out *connect.ServerStream[pb.Event]) error {
	parts := strings.Split(in.Msg.Route, "/")
	if len(parts) != 4 || parts[1] != "v1" || !scopeName.MatchString(parts[3]) || !(parts[2] == "planning" || parts[2] == "approvals" || parts[2] == "recovery-jobs") {
		return publicRPCError(model.Err("INVALID_REQUEST", "Invalid receipt route"))
	}
	if ctx.Value(fleetContext{}) != nil && !model.FleetRoute(in.Msg.Route) {
		return publicRPCError(model.Err("SCOPE_DENIED", "Invalid delegated job route"))
	}
	if in.Msg.Host != "" {
		return s.forwardWatch(ctx, in.Msg.Host, in.Msg.Route, nil, in.Msg, out)
	}
	r, err := logicalRequest(ctx, "GET", in.Msg.Route, nil)
	if err != nil {
		return watchRPCError(err)
	}
	if err := s.c.statusAuthority(r, s.local); err != nil {
		return watchRPCError(err)
	}
	if !s.c.observations.reserveStatusStream() {
		return publicRPCError(model.Err("RATE_LIMITED", "Watch capacity exceeded"))
	}
	defer s.c.observations.releaseStatusStream()
	send := streamSender(ctx, out)
	tick := time.NewTicker(eventStreamHeartbeat)
	defer tick.Stop()
	var prev []byte
	for {
		data, changed, state, err := s.jobSnapshot(r)
		if err != nil {
			return watchRPCError(err)
		}
		if !bytes.Equal(prev, data) {
			if err := send(&pb.Event{Kind: "receipt", JsonData: data}); err != nil {
				return err
			}
			prev = data
		}
		switch state.Status {
		case "running", "queued", "pending":
		default:
			return nil
		}
		var timer *time.Timer
		var expiry <-chan time.Time
		if state.ExpiresAt != 0 && state.Status == "pending" {
			timer = time.NewTimer(time.UnixMilli(state.ExpiresAt).Sub(s.c.opts.Now()))
			expiry = timer.C
		}
		err = s.waitChange(r, changed, expiry, tick.C, send)
		if timer != nil {
			timer.Stop()
		}
		if err != nil {
			return err
		}
	}
}

type receiptState struct {
	Status    string
	ExpiresAt int64
}

func (s controlService) jobSnapshot(r *http.Request) ([]byte, <-chan struct{}, receiptState, error) {
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, receiptState{}, model.Err("UNAVAILABLE", "Controller stopped")
	}
	value, err := c.dispatch(r, s.local)
	if err != nil {
		return nil, nil, receiptState{}, err
	}
	if approval, ok := value.(map[string]any); ok && approval["status"] == "pending" {
		if expires, ok := approval["expiresAt"].(int64); ok && expires <= c.opts.Now().UnixMilli() {
			approval["status"] = "expired"
		}
	}
	data, err := json.Marshal(value)
	var receipt receiptState
	if err == nil {
		err = json.Unmarshal(data, &receipt)
	}
	if c.jobChange == nil {
		c.jobChange = make(chan struct{})
	}
	return data, c.jobChange, receipt, err
}

func (c *Controller) signalJobChange() {
	if c.jobChange != nil {
		close(c.jobChange)
		c.jobChange = nil
	}
}
