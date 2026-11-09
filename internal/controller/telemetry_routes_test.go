package controller

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTelemetryAdmission authenticates before body allocation and enforces a separate ingest budget.
func TestTelemetryAdmission(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	_, created := call(t, c, true, "POST", "/v1/telemetry/collectors", map[string]any{"name": "limits"}, "")
	token := created["token"].(string)
	// Unauthenticated clients cannot use parsing limits to bypass authentication.
	if code, _ := uploadTelemetry(c, "invalid", strings.Repeat("x", telemetryPayloadLimit+1), "application/json"); code != 401 {
		t.Fatal("unauthenticated oversized body was parsed")
	}
	// Authenticated bodies remain bounded before allocating a decoder tree.
	if code, _ := uploadTelemetry(c, token, strings.Repeat("x", telemetryPayloadLimit+1), "application/json"); code != 400 {
		t.Fatal("oversized body accepted")
	}
	c.telemetry.reserve()
	c.telemetry.reserve()
	// Two occupied ingestion slots reject additional work without blocking management.
	if code, _ := uploadTelemetry(c, token, "{}", "application/json"); code != 429 {
		t.Fatal("ingestion concurrency was not bounded")
	}
	c.telemetry.release()
	c.telemetry.release()
	request := httptest.NewRequest("POST", "/v1/telemetry/otlp/logs", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	c.Handler().ServeHTTP(response, request)
	// A compressed body must not be mistaken for the supported plain JSON encoding.
	if response.Code != 400 {
		t.Fatal("unsupported compression accepted")
	}
}

// TestTelemetryQueryBounds rejects malformed selectors before querying history.
func TestTelemetryQueryBounds(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	// Every malformed selector is rejected before querying any sensitive history.
	for _, query := range []string{"signal=other", "signal=logs&limit=201", "signal=logs&limit=0", "signal=metrics&before=-1"} {
		status, _ := call(t, c, true, "GET", "/v1/telemetry/history?"+query, nil, "")
		// Invalid cursors, limits, and signals are client errors rather than defaulted values.
		if status != 400 {
			t.Fatalf("accepted malformed query %s", query)
		}
	}
}

// TestTelemetryStorageFailureIsRetryable distinguishes invalid requests from temporary history failures.
func TestTelemetryStorageFailureIsRetryable(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	_, created := call(t, c, true, "POST", "/v1/telemetry/collectors", map[string]any{"name": "retry"}, "")
	token := created["token"].(string)
	// Syntactically invalid OTLP is a permanent client error, independent of storage health.
	if code, _ := uploadTelemetry(c, token, "{", "application/json"); code != 400 {
		t.Fatal("malformed request must not be retried", code)
	}
	store, err := c.telemetryStore()
	// Open the real independent history before injecting an unavailable database.
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	// A valid empty export reaches storage and must instruct the collector to retry its batch.
	if code, body := uploadTelemetry(c, token, `{"resourceLogs":[]}`, "application/json"); code != 503 || !strings.Contains(body, "TELEMETRY_UNAVAILABLE") {
		t.Fatalf("temporary store failure is not retryable: %d %s", code, body)
	}
	// Invalid payloads remain client errors even when the already-open store is unavailable.
	if code, _ := uploadTelemetry(c, token, "{", "application/json"); code != 400 {
		t.Fatal("malformed request turned into a transient store failure", code)
	}
}
