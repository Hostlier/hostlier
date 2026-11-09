package controller

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
	"hostlier/internal/telemetry"
)

// telemetryTestRuntime resolves one owned identity while refusing arbitrary resource labels.
type telemetryTestRuntime struct{ recordingRuntime }

// ResolveTelemetry grants the fixture's exact container identity only.
func (r *telemetryTestRuntime) ResolveTelemetry(_ context.Context, attrs map[string]string) (model.TelemetryIdentity, error) {
	// The fixture admits one exact owned instance; every other resource claim is rejected.
	if attrs["container.id"] == "owned" {
		return model.TelemetryIdentity{Application: "sample", Revision: "current", Component: "backend", Instance: "owned"}, nil
	}
	return model.TelemetryIdentity{}, model.Err("UNAUTHORIZED", "Unknown runtime identity")
}

// telemetryCacheRuntime admits one exact Kubernetes workload for cache-key collision coverage.
type telemetryCacheRuntime struct{ recordingRuntime }

// ResolveTelemetry grants only complete coordinates, independently of the caller's node label.
func (r *telemetryCacheRuntime) ResolveTelemetry(_ context.Context, attrs map[string]string) (model.TelemetryIdentity, error) {
	// The altered namespace in the forged resource is never valid workload identity.
	if attrs["k8s.namespace.name"] != "ns" || attrs["k8s.pod.name"] != "pod" || attrs["k8s.pod.uid"] != "uid" || attrs["k8s.container.name"] != "worker" {
		return model.TelemetryIdentity{}, model.Err("TELEMETRY_IDENTITY", "Unknown workload")
	}
	return model.TelemetryIdentity{Application: "sample", Revision: "current", Component: "worker", Instance: "uid"}, nil
}

// TestTelemetryResolverCacheSeparatesResourceCoordinates prevents delimiter-based attribution reuse.
func TestTelemetryResolverCacheSeparatesResourceCoordinates(t *testing.T) {
	c, err := New(Options{StateDir: t.TempDir(), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: &telemetryCacheRuntime{}})
	// Use the real controller resolver with an exact-coordinate runtime fixture.
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	c.state.Applications["sample"] = &Application{Deployment: model.Deployment{Name: "sample", RuntimeID: "current"}, Revision: "approved-revision"}
	resolve := c.telemetryResolver(context.Background())
	valid := telemetry.Resource{"k8s.namespace.name": "ns", "k8s.pod.name": "pod", "k8s.pod.uid": "uid", "k8s.container.name": "worker", "k8s.node.name": "|||fake"}
	workload, err := resolve(valid)
	// The first resource must populate the per-request cache with a verified workload identity.
	if err != nil || workload.Application != "sample" {
		t.Fatalf("valid resource: %+v %v", workload, err)
	}
	forged := telemetry.Resource{"k8s.namespace.name": "ns|pod|uid|worker", "k8s.node.name": "fake"}
	node, err := resolve(forged)
	// These resources produce the same pipe-joined string but have different identity coordinates.
	if err != nil || node.Application != "" || node.Component != "" || node.Node != "fake" {
		t.Fatalf("node-only resource inherited workload identity: %+v %v", node, err)
	}
}

// telemetryLogBody builds a real OTLP JSON log record with a caller-controlled resource claim.
func telemetryLogBody(container string) string {
	attributes := []any{
		map[string]any{"key": "container.id", "value": map[string]any{"stringValue": container}},
		map[string]any{"key": "io.hostlier.app", "value": map[string]any{"stringValue": "victim"}},
	}
	record := map[string]any{"timeUnixNano": strconv.FormatInt(time.Now().UnixNano(), 10), "body": map[string]any{"stringValue": "PRIVATE_TEST_LOG"}}
	scope := map[string]any{"logRecords": []any{record}}
	resource := map[string]any{"resource": map[string]any{"attributes": attributes}, "scopeLogs": []any{scope}}
	raw, _ := json.Marshal(map[string]any{"resourceLogs": []any{resource}})
	return string(raw)
}

// uploadTelemetry sends the collector wire format through the actual remote HTTP handler.
func uploadTelemetry(c *Controller, token, body, contentType string) (int, string) {
	request := httptest.NewRequest("POST", "/v1/telemetry/otlp/logs", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	c.Handler().ServeHTTP(response, request)
	return response.Code, response.Body.String()
}

// TestTelemetryOwnedHistory verifies ingestion identity, private history, and independence from command persistence.
func TestTelemetryOwnedHistory(t *testing.T) {
	c, err := New(Options{StateDir: t.TempDir(), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: &telemetryTestRuntime{}})
	// Exercise ingestion through the full controller with runtime-backed attribution.
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	c.state.Applications["sample"] = &Application{Deployment: model.Deployment{Name: "sample", RuntimeID: "current"}, Spec: project()}
	c.mu.Lock()
	c.state.Applications["sample"].RuntimeID = "current"
	c.state.Applications["sample"].Revision = "approved-revision"
	c.mu.Unlock()
	status, created := call(t, c, true, "POST", "/v1/telemetry/collectors", map[string]any{"name": "collector"}, "")
	// Local issuance establishes the separate collector authority for this export.
	if status != 200 {
		t.Fatal(created)
	}
	token := created["token"].(string)
	// The verified workload record must be accepted without a partial-success rejection.
	if code, body := uploadTelemetry(c, token, telemetryLogBody("owned"), "application/json"); code != 200 || strings.Contains(body, "partialSuccess") {
		t.Fatalf("ingest %d %s", code, body)
	}
	// Foreign workload claims are rejected without turning the whole batch into a server error.
	if code, body := uploadTelemetry(c, token, telemetryLogBody("foreign"), "application/json"); code != 200 || !strings.Contains(body, "partialSuccess") {
		t.Fatalf("foreign record %d %s", code, body)
	}
	verifyTelemetryHistoryIsolation(t, c, token)
}

// verifyTelemetryHistoryIsolation checks privacy, revision attribution, and independent storage failures.
func verifyTelemetryHistoryIsolation(t *testing.T, c *Controller, token string) {
	t.Helper()
	status, page := call(t, c, true, "GET", "/v1/telemetry/history?signal=logs&app=sample", nil, "")
	raw, _ := json.Marshal(page)
	// History must retain both the sensitive payload and the controller's approved revision.
	if status != 200 || !strings.Contains(string(raw), "approved-revision") || !strings.Contains(string(raw), "PRIVATE_TEST_LOG") {
		t.Fatalf("history %d %s", status, raw)
	}
	// Snapshot observers must never receive application log bodies through public status.
	if strings.Contains(c.latestStatus(), "PRIVATE_TEST_LOG") {
		t.Fatal("log leaked into status stream")
	}
	// The transport must explicitly reject wire formats the implementation cannot decode.
	if code, _ := uploadTelemetry(c, token, "{}", "application/x-protobuf"); code != 400 {
		t.Fatal("accepted unsupported encoding")
	}
	// Ingest must work even if the separate command state is unavailable for writes.
	c.store.Close()
	// Telemetry persistence remains independent when authoritative command storage is unavailable.
	if code, body := uploadTelemetry(c, token, telemetryLogBody("owned"), "application/json"); code != 200 {
		t.Fatalf("ingest requires command persistence: %d %s", code, body)
	}
}
