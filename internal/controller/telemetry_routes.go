package controller

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"hostlier/internal/model"
)

const telemetryPayloadLimit = 1 << 20

// serveTelemetry separates diagnostic ingestion/history from durable mutation dispatch.
func (c *Controller) serveTelemetry(w http.ResponseWriter, r *http.Request, local bool) bool {
	// Match the complete route before invoking its dedicated handler.
	if !strings.HasPrefix(r.URL.Path, "/v1/telemetry/") {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	var result any
	var err error
	// Keep each telemetry operation on its own authorization and I/O path.
	switch {
	// Dispatch only this supported telemetry operation.
	case r.URL.Path == "/v1/telemetry/collectors" || strings.HasPrefix(r.URL.Path, "/v1/telemetry/collectors/"):
		r.Body = http.MaxBytesReader(w, r.Body, 131072)
		result, err = c.telemetryCredentials(r, local)
	// Dispatch only this supported telemetry operation.
	case telemetryUploadRoute(r):
		result, err = c.receiveTelemetry(w, r)
	// Dispatch only this supported telemetry operation.
	case r.Method == "GET" && r.URL.Path == "/v1/telemetry/history":
		// Propagate failure without publishing an incomplete or misleading result.
		if err = c.statusAuthority(r, local); err == nil {
			result, err = c.queryTelemetry(r)
		}
	// Unknown routes never inherit another operation’s authority.
	default:
		result, err = endpointNotFound()
	}
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil {
		eventStreamError(w, err)
		return true
	}
	json.NewEncoder(w).Encode(result)
	return true
}

// receiveTelemetry authenticates before allocation and rejects unsupported OTLP encodings explicitly.
func (c *Controller) receiveTelemetry(w http.ResponseWriter, r *http.Request) (any, error) {
	credential, err := c.telemetryAuthority(r)
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil {
		return nil, err
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil || media != "application/json" {
		return nil, model.Err("INVALID_REQUEST", "OTLP HTTP JSON is required")
	}
	encoding := r.Header.Get("Content-Encoding")
	// Reject unsupported compression instead of parsing ambiguous bytes.
	if encoding != "" && encoding != "identity" {
		return nil, model.Err("INVALID_REQUEST", "Compressed OTLP requests are not supported")
	}
	// Backpressure limits concurrent bodies and ownership probes.
	if !c.telemetry.reserve() {
		return nil, model.Err("RATE_LIMITED", "Telemetry ingestion capacity exceeded")
	}
	defer c.telemetry.release()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, telemetryPayloadLimit))
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil {
		return nil, model.Err("INVALID_REQUEST", "Telemetry request exceeds size limit or cannot be read")
	}
	// Propagate failure without publishing an incomplete or misleading result.
	if _, err = c.telemetryAuthority(r); err != nil {
		return nil, err
	}
	return c.ingestTelemetry(r, credential, strings.TrimPrefix(r.URL.Path, "/v1/telemetry/otlp/"), body)
}

// telemetryUploadRoute only admits the two implemented OTLP signals and the POST method.
func telemetryUploadRoute(r *http.Request) bool {
	return r.Method == "POST" && (r.URL.Path == "/v1/telemetry/otlp/logs" || r.URL.Path == "/v1/telemetry/otlp/metrics")
}
