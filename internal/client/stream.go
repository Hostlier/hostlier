package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hostlier/internal/model"
)

// WatchOperation resumes bounded SSE connections until a terminal snapshot is consumed or observation is cancelled.
func (c *Client) WatchOperation(ctx context.Context, operationID, cursor string, consume func(OperationEvent) error) error {
	watch, err := c.operationWatcher(operationID, cursor, consume)
	// Reject invalid resume state before entering the reconnect loop.
	if err != nil {
		return err
	}
	delay := 100 * time.Millisecond
	// Per-connection HTTP timeouts do not impose a wall-clock limit on durable server work.
	for {
		// Cancellation stops only observation; it never sends a server cancellation request.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		before := watch.sequence
		err := watch.connect(ctx)
		// Successful return is reserved for a consumed terminal operation snapshot.
		if err == nil {
			return nil
		}
		// Semantic, authentication, TLS and consumer failures are final; only transport drops reconnect.
		if err = streamObservationError(err); err != nil {
			return err
		}
		// Real progress resets backoff while repeated empty drops remain bounded.
		if watch.sequence > before {
			delay = 100 * time.Millisecond
		}
		// Preserve prompt cancellation even while waiting to reconnect.
		if err = streamBackoff(ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, 2*time.Second)
	}
}

// connect opens one credential-scoped stream using the same verified transport as ordinary API requests.
func (w *operationWatch) connect(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", w.client.URL+"/v1/operations/"+url.PathEscape(w.operationID)+"/events", nil)
	// Invalid local request configuration is permanent and must not enter a retry loop.
	if err != nil {
		return model.Err("EVENT_PROTOCOL", "Cannot construct operation event request")
	}
	req.Header.Set("Accept", "text/event-stream")
	// Resume only from the last event acknowledged by the consumer.
	if w.cursor != "" {
		req.Header.Set("Last-Event-ID", w.cursor)
	}
	// Anonymous local clients omit bearer credentials entirely.
	if err := w.client.authorizeRequest(req, nil); err != nil {
		return err
	}
	transport := *w.client.HTTP
	// Do not forward bearer tokens even if an injected HTTP client follows redirects by default.
	transport.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := transport.Do(req)
	// Distinguish certificate/authentication configuration from retryable interrupted connections.
	if err != nil {
		return streamConnectionError(ctx, err)
	}
	defer response.Body.Close()
	// Legacy fallback requires proving the operation exists independently of the absent endpoint.
	if response.StatusCode == 404 || response.StatusCode == 405 {
		return w.unsupported(ctx)
	}
	// Decode bounded API errors before interpreting a response as an SSE stream.
	if response.StatusCode != 200 {
		return operationHTTPError(response)
	}
	return w.readResponse(response)
}

// unsupported distinguishes an older controller endpoint from a genuinely unknown operation.
func (w *operationWatch) unsupported(ctx context.Context) error {
	var operation operationSnapshot
	// Missing or unauthorized operations must retain their real semantic failure instead of entering polling fallback.
	if err := w.client.Request(ctx, "GET", "/v1/operations/"+url.PathEscape(w.operationID), nil, &operation); err != nil {
		return err
	}
	// Require a matching real operation before declaring the streaming capability absent.
	if operation.ID != w.operationID {
		return model.Err("EVENT_PROTOCOL", "Controller returned a different operation")
	}
	return ErrEventsUnsupported
}

// readResponse enforces an SSE media type and returns retryable EOF only for incomplete observation.
func (w *operationWatch) readResponse(response *http.Response) error {
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	// HTML login pages and JSON success bodies cannot be interpreted as operation progress.
	if err != nil || media != "text/event-stream" {
		return model.Err("EVENT_PROTOCOL", "Controller did not return an operation event stream")
	}
	return w.readOperationFrames(response.Body)
}

// streamConnectionError normalizes permanent TLS failures while allowing transient network drops to resume.
func streamConnectionError(ctx context.Context, err error) error {
	// Preserve cancellation identity so callers can report durable work as pending.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var certificate *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	// Never reconnect indefinitely when the controller's verified certificate is invalid.
	if errors.As(err, &certificate) || errors.As(err, &authority) || errors.As(err, &hostname) {
		return model.Err("CONNECTION", "Cannot verify controller HTTPS certificate")
	}
	var network net.Error
	// Connection errors and request deadlines are retryable; semantic request construction errors are not.
	if errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return io.EOF
	}
	return model.Err("CONNECTION", "Cannot open controller operation event stream")
}

// streamBackoff waits between reconnects without delaying cancellation or changing server operation state.
func streamBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	// Select observation cancellation independently of the durable operation's own lifetime.
	select {
	// Stop reconnect attempts as soon as the caller stops watching.
	case <-ctx.Done():
		return ctx.Err()
	// Reopen the stream only after bounded backoff.
	case <-timer.C:
		return nil
	}
}

// operationWatcher validates local identity and cursor state before opening any controller connection.
func (c *Client) operationWatcher(operationID, cursor string, consume func(OperationEvent) error) (*operationWatch, error) {
	// Validate path identity and consumer presence before sending credentials to the configured controller.
	if operationID == "" || strings.ContainsAny(operationID, "/:\x00\r\n") || consume == nil {
		return nil, model.Err("EVENT_PROTOCOL", "Operation identity and event consumer are required")
	}
	watch := &operationWatch{client: c, operationID: operationID, cursor: cursor, consume: consume}
	// An explicit resume token must be valid before opening any network connection.
	if cursor != "" {
		sequence, err := operationCursor(cursor, operationID)
		// Do not acknowledge a malformed or cross-operation resume token.
		if err != nil {
			return nil, err
		}
		watch.sequence = sequence
	}
	return watch, nil
}

// operationHTTPError rejects unexpected successful statuses and preserves bounded structured API errors.
func operationHTTPError(response *http.Response) error {
	// Intermediary gateway failures are transient and carry no acknowledged operation progress.
	switch response.StatusCode {
	// Reopen the same operation stream with its last consumed cursor after bounded backoff.
	case 502, 503, 504:
		return io.EOF
	}
	// SSE requires exactly200 rather than a different successful non-streaming response.
	if response.StatusCode < 300 {
		return model.Err("EVENT_PROTOCOL", "Unexpected operation event response status")
	}
	return decodeResponse(response, "/v1/operations", nil)
}

// streamObservationError separates local delivery failures from connection drops that permit reconnecting.
func streamObservationError(err error) error {
	var consumer *eventConsumerFailure
	// Return the exact caller error even when it wraps EOF or a network-looking failure.
	if errors.As(err, &consumer) {
		return consumer.cause
	}
	// Only transport disconnects and explicitly retryable gateway responses reach the reconnect loop.
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
