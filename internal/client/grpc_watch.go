package client

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/model"
)

func (w *operationWatch) connectRPC(ctx context.Context) error {
	call := connect.NewRequest(&pb.EventRequest{OperationId: w.operationID, Cursor: w.cursor, Host: w.host})
	if err := w.client.signedWatch(ctx, hostRoute(w.host, "/v1/operations/"+url.PathEscape(w.operationID)+"/events"), call.Header()); err != nil {
		return err
	}
	stream, err := w.client.watchService().WatchOperation(ctx, call)
	if err != nil {
		return streamEnd(ctx, err)
	}
	defer stream.Close()
	for stream.Receive() {
		event := stream.Msg()
		if event.Kind == "heartbeat" {
			continue
		}
		if len(event.JsonData) == 0 || len(event.JsonData) > MaxOperationFrameBytes {
			return model.Err("EVENT_PROTOCOL", "Invalid operation event size")
		}
		if err := w.consumeFrame(operationFrame{event: event.Kind, id: event.Cursor, hasID: event.Cursor != "", data: []string{string(event.JsonData)}}); err != nil {
			return err
		}
		if w.terminal {
			return nil
		}
	}
	return streamEnd(ctx, stream.Err())
}

func (c *Client) connectStatusRPC(ctx context.Context, consume func(StatusEvent) error) error {
	call := connect.NewRequest(&pb.StatusRequest{})
	if err := c.signedWatch(ctx, "/v1/status/events", call.Header()); err != nil {
		return err
	}
	stream, err := c.watchService().WatchStatus(ctx, call)
	if err != nil {
		return streamEnd(ctx, err)
	}
	defer stream.Close()
	for stream.Receive() {
		event := stream.Msg()
		if event.Kind == "heartbeat" {
			continue
		}
		if err := consumeStatusFrame(operationFrame{event: event.Kind, data: []string{string(event.JsonData)}}, consume); err != nil {
			return err
		}
	}
	return streamEnd(ctx, stream.Err())
}

func hostRoute(host, path string) string {
	if host != "" && host != "local" {
		return "/v1/hosts/" + url.PathEscape(host) + "/proxy/" + strings.TrimPrefix(path, "/v1/")
	}
	return path
}

// WatchJob reconnects to the same retained receipt until a terminal snapshot.
// Each new subscription starts with current state, so no polling or cursor is needed.
func (c *Client) WatchJob(ctx context.Context, host, path string, consume func(json.RawMessage) error) error {
	if host == "local" {
		host = ""
	}
	delay := 100 * time.Millisecond
	for {
		err := c.jobSubscription(ctx, host, path, consume)
		if err == nil {
			return nil
		}
		if final := streamObservationError(err); final != nil {
			return final
		}
		if err := streamBackoff(ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, 2*time.Second)
	}
}

func (c *Client) jobSubscription(ctx context.Context, host, path string, consume func(json.RawMessage) error) error {
	call := connect.NewRequest(&pb.JobRequest{Route: path, Host: host})
	if err := c.signedWatch(ctx, hostRoute(host, path), call.Header()); err != nil {
		return err
	}
	stream, err := c.watchService().WatchJob(ctx, call)
	if err != nil {
		return streamEnd(ctx, err)
	}
	defer stream.Close()
	for stream.Receive() {
		m := stream.Msg()
		if m.Kind == "heartbeat" {
			continue
		}
		var view struct{ ID, Status string }
		if m.Kind != "receipt" || json.Unmarshal(m.JsonData, &view) != nil || !strings.HasSuffix(path, "/"+url.PathEscape(view.ID)) || view.ID == "" || view.Status == "" {
			return model.Err("EVENT_PROTOCOL", "Invalid job receipt")
		}
		if err := consume(m.JsonData); err != nil {
			return &eventConsumerFailure{cause: err}
		}
		switch view.Status {
		case "running", "pending", "queued":
		default:
			return nil
		}
	}
	return streamEnd(ctx, stream.Err())
}
