package telemetry

import (
	"bytes"
	"encoding/json"
	"io"
)

// decodeEnvelope bounds JSON work before allocating signal-specific records.
func decodeEnvelope(signal string, body []byte, scope Scope, resolve Resolver) (envelope, error) {
	// Reject request size before accepting untrusted telemetry.
	if len(body) > MaxBodyBytes || len(body) == 0 {
		return envelope{}, invalid("request size")
	}
	// Reject collector scope before accepting untrusted telemetry.
	if !validScope(scope, resolve) {
		return envelope{}, invalid("collector scope")
	}
	// Stop on failed validation or I/O before exposing a successful observation.
	if err := jsonBounds(body); err != nil {
		return envelope{}, err
	}
	var raw map[string]json.RawMessage
	// Reject JSON object required before accepting untrusted telemetry.
	if json.Unmarshal(body, &raw) != nil {
		return envelope{}, invalid("JSON object required")
	}
	expected := map[string]string{"logs": "resourceLogs", "metrics": "resourceMetrics"}[signal]
	// Reject signal envelope before accepting untrusted telemetry.
	if expected == "" || raw[expected] == nil || len(raw) > 1 {
		return envelope{}, invalid("signal envelope")
	}
	var e envelope
	// Reject OTLP JSON shape before accepting untrusted telemetry.
	if json.Unmarshal(body, &e) != nil {
		return envelope{}, invalid("OTLP JSON shape")
	}
	return e, nil
}

// validScope requires caller-supplied authenticated collector metadata.
func validScope(scope Scope, resolve Resolver) bool {
	return scope.Collector != "" && len(scope.Collector) <= 256 && len(scope.Runtime) <= 32 && resolve != nil
}

// jsonBounds rejects excessive nesting and large empty-container floods before decoding.
func jsonBounds(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	depth := 0
	containers := 0
	// Consume input incrementally while enforcing the finite structural budget.
	for {
		token, err := decoder.Token()
		// Stop on failed validation or I/O before exposing a successful observation.
		if err == io.EOF {
			return nil
		}
		// Reject JSON syntax before accepting untrusted telemetry.
		if err != nil {
			return invalid("JSON syntax")
		}
		delim, ok := token.(json.Delim)
		// Skip absent values without inventing observations.
		if !ok {
			continue
		}
		// Apply the bounded special case before continuing record processing.
		if delim == '{' || delim == '[' {
			depth++
			containers++
		} else {
			depth--
		}
		// Reject JSON structure bounds before accepting untrusted telemetry.
		if depth > 32 || containers > 8192 {
			return invalid("JSON structure bounds")
		}
	}
}
