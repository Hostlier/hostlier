package client

import (
	"context"
	"mime"
	"net/http"
	"time"

	"hostlier/internal/model"
)

// StatusEvent separates transport availability from the timestamp of the last completed capture.
type StatusEvent struct {
	Reset     bool                  `json:"reset,omitempty"`
	Connected bool                  `json:"connected"`
	Snapshot  *model.StatusSnapshot `json:"snapshot,omitempty"`
}

// WatchStatus shares verified HTTPS transport and reconnects to full replacement observations.
func (c *Client) WatchStatus(ctx context.Context, consume func(StatusEvent) error) error {
	// A nil consumer cannot acknowledge observations safely.
	if consume == nil {
		return model.Err("EVENT_PROTOCOL", "Status consumer required")
	}
	delay := 100 * time.Millisecond
	// No cursor is required because every connection starts with a full replacement.
	for {
		err := c.connectStatus(ctx, consume)
		// Consumer failures and permanent authentication/protocol failures must never be retried.
		if failure := streamObservationError(err); failure != nil {
			return failure
		}
		// Connection loss is visible even when an older cached snapshot remains useful.
		if err = consume(StatusEvent{Connected: false}); err != nil {
			return err
		}
		// Cancellation interrupts reconnect delay without canceling any controller operation.
		if err = streamBackoff(ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, 2*time.Second)
	}
}

// connectStatus opens one bounded SSE connection without forwarding credentials across redirects.
func (c *Client) connectStatus(ctx context.Context, consume func(StatusEvent) error) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.URL+"/v1/status/events", nil)
	// Invalid client configuration is a permanent local failure.
	if err != nil {
		return model.Err("EVENT_PROTOCOL", "Invalid status endpoint")
	}
	req.Header.Set("Accept", "text/event-stream")
	// Protected Unix clients omit remote bearer authentication.
	if err := c.authorizeRequest(req, nil); err != nil {
		return err
	}
	transport := *c.HTTP
	// Even injected transports must not redirect bearer authority.
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := transport.Do(req)
	// Certificate failures are permanent; network interruptions may reconnect.
	if err != nil {
		return streamConnectionError(ctx, err)
	}
	defer response.Body.Close()
	// Only exact successful streaming responses may supply observations.
	if response.StatusCode != http.StatusOK {
		return operationHTTPError(response)
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	// An HTML proxy page must not become browser-visible deployment status.
	if err != nil || media != "text/event-stream" {
		return model.Err("EVENT_PROTOCOL", "Controller did not return status events")
	}
	return readStatusFrames(response.Body, consume)
}
