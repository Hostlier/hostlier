package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"

	"testing"
	"time"
)

// discardOperationEvent acknowledges fixture events without altering their cursor or payload.
func discardOperationEvent(OperationEvent) error { return nil }

// TestStreamSemanticResponsesDoNotRetry protects authorization, cursor and redirect boundaries.
func TestStreamSemanticResponsesDoNotRetry(t *testing.T) {
	// None of these responses is an interrupted successful stream eligible for automatic reconnection.
	for _, status := range []int{400, 401, 403, 404, 405, 204, 302, 500} {
		checkPermanentStreamStatus(t, status)
	}
}

// checkPermanentStreamStatus requires one safe error and no credential-forwarding redirect request.
func checkPermanentStreamStatus(t *testing.T, status int) {
	calls := 0
	// A redirect target must never receive the bearer token, even with a permissive injected client.
	c := &Client{URL: "https://controller.test", Token: "private-token", HTTP: &http.Client{Transport: eventTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		response := eventResponse(status, `{"error":{"code":"DENIED","message":"Request denied"}}`)
		response.Header.Set("Location", "https://untrusted.test/events")
		return response, nil
	})}}
	err := c.WatchOperation(context.Background(), "job", "", discardOperationEvent)
	// Semantic failures stop observation immediately without trying to reinterpret an HTTP success code.
	if err == nil || calls != 1 {
		t.Fatalf("status%d was retried or accepted", status)
	}
}

// TestStreamRetriesNetworkDropsButNotInvalidTLS keeps resumability separate from certificate verification.
func TestStreamRetriesNetworkDropsButNotInvalidTLS(t *testing.T) {
	checkStreamTransportFailure(t, &net.OpError{Op: "read", Net: "tcp", Err: io.EOF}, true)
	checkStreamTransportFailure(t, &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, false)
}

// checkStreamTransportFailure supplies one transport failure followed by a terminal snapshot if retry is allowed.
func checkStreamTransportFailure(t *testing.T, failure error, retry bool) {
	calls := 0
	// A second request can succeed only when the first failure was transient rather than an invalid certificate.
	c := &Client{URL: "https://controller.test", HTTP: &http.Client{Transport: eventTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		// The first connection supplies the intended transport failure; only a permitted retry reaches completion.
		if calls == 1 {
			return nil, failure
		}
		return eventResponse(200, statusEvent("succeeded")), nil
	})}}
	err := c.WatchOperation(context.Background(), "job", "", discardOperationEvent)
	// Verified TLS failures must remain permanent while transient connection loss resumes safely.
	if (err == nil) != retry || (calls == 2) != retry {
		t.Fatal("incorrect transport retry classification")
	}
}

// TestWatchRejectsMalformedResumeBeforeNetwork prevents a cross-operation cursor from reaching transport.
func TestWatchRejectsMalformedResumeBeforeNetwork(t *testing.T) {
	calls := 0
	// Any transport invocation would demonstrate failure to validate local resume state first.
	c := &Client{URL: "https://controller.test", HTTP: &http.Client{Transport: eventTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return eventResponse(200, statusEvent("succeeded")), nil
	})}}
	err := c.WatchOperation(context.Background(), "job", "other:1", discardOperationEvent)
	// Invalid cursors are semantic input failures with no authenticated request side effects.
	if !eventErrorCode(err, "INVALID_EVENT_CURSOR") || calls != 0 {
		t.Fatal("malformed cursor reached controller")
	}
}

// TestConsumerEOFFailureDoesNotReconnect keeps an output EOF distinct from a transport EOF.
func TestConsumerEOFFailureDoesNotReconnect(t *testing.T) {
	// Exercise both the direct writer sentinel and an application error wrapping that sentinel.
	for _, failure := range []error{io.EOF, fmt.Errorf("output closed: %w", io.EOF)} {
		checkConsumerEOF(t, failure)
	}
}

// checkConsumerEOF bounds a regression that would otherwise reconnect forever after failed delivery.
func checkConsumerEOF(t *testing.T, failure error) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	calls := 0
	// Every unnecessary retry would replay the same unacknowledged log.
	c := &Client{URL: "https://controller.test", HTTP: &http.Client{Transport: eventTransport(func(*http.Request) (*http.Response, error) { calls++; return eventResponse(200, logEvent(1)), nil })}}
	// A local consumer EOF must be returned immediately, with no reconnect or cursor acknowledgement.
	err := c.WatchOperation(ctx, "job", "", func(OperationEvent) error { return failure })
	// Preserve the exact application failure instead of replacing it with eventual cancellation.
	if err != failure || calls != 1 {
		t.Fatal("consumer EOF triggered a reconnect")
	}
}

// TestTransientGatewayFailuresReconnect retries bounded intermediary outages without retrying semantic API failures.
func TestTransientGatewayFailuresReconnect(t *testing.T) {
	// Gateway failure codes may occur before the controller stream is reached.
	for _, status := range []int{502, 503, 504} {
		checkGatewayReconnect(t, status)
	}
}

// checkGatewayReconnect supplies one transient intermediary failure and then a complete terminal stream.
func checkGatewayReconnect(t *testing.T, status int) {
	calls := 0
	// No failed gateway response may advance the durable log cursor.
	c := &Client{URL: "https://controller.test", HTTP: &http.Client{Transport: eventTransport(func(*http.Request) (*http.Response, error) {
		calls++
		// Only the first response models the temporary intermediary outage.
		if calls == 1 {
			return eventResponse(status, "intermediary failure"), nil
		}
		return eventResponse(200, statusEvent("succeeded")), nil
	})}}
	err := c.WatchOperation(context.Background(), "job", "", discardOperationEvent)
	// Completion must come from the terminal stream after one bounded reconnect.
	if err != nil || calls != 2 {
		t.Fatalf("gateway%d did not resume: %v", status, err)
	}
}
