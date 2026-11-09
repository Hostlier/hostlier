package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// ErrInvalid distinguishes permanent malformed input from retryable storage failures.
var ErrInvalid = errors.New("invalid telemetry")

// attribute is an OTLP AnyValue keyed attribute.
type attribute struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// envelope separates accepted signals without decoding arbitrary payload trees.
type envelope struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
	ResourceLogs    []resourceLogs    `json:"resourceLogs"`
}

// resourceAttributes supplies resource-level metadata without trusting ownership labels.
type resourceAttributes struct {
	Attributes []attribute `json:"attributes"`
}

// resourceMetrics holds standard OTLP metric scope groups.
type resourceMetrics struct {
	Resource resourceAttributes `json:"resource"`
	Scopes   []scopeMetrics     `json:"scopeMetrics"`
}

// scopeMetrics groups a bounded collection of metric definitions.
type scopeMetrics struct {
	Metrics []metric `json:"metrics"`
}

// metric accepts scalar gauge/sum observations and identifies unsupported distributions.
type metric struct {
	Name                 string  `json:"name"`
	Unit                 string  `json:"unit"`
	Gauge                *points `json:"gauge"`
	Sum                  *points `json:"sum"`
	Histogram            *points `json:"histogram"`
	ExponentialHistogram *points `json:"exponentialHistogram"`
	Summary              *points `json:"summary"`
}

// points retains raw samples so distribution samples can be rejected explicitly.
type points struct {
	Data        []json.RawMessage `json:"dataPoints"`
	Temporality int               `json:"aggregationTemporality"`
	Monotonic   bool              `json:"isMonotonic"`
}

// numberPoint defines standard OTLP scalar measurement fields.
type numberPoint struct {
	Time       json.RawMessage `json:"timeUnixNano"`
	Start      json.RawMessage `json:"startTimeUnixNano"`
	Double     *float64        `json:"asDouble"`
	Int        json.RawMessage `json:"asInt"`
	Attributes []attribute     `json:"attributes"`
}

// resourceLogs contains standard OTLP log scope groups.
type resourceLogs struct {
	Resource resourceAttributes `json:"resource"`
	Scopes   []scopeLogs        `json:"scopeLogs"`
}

// scopeLogs groups records emitted by one instrumentation scope.
type scopeLogs struct {
	Logs []logRecord `json:"logRecords"`
}

// logRecord retains message severity and correlation as sensitive attributes.
type logRecord struct {
	Time           json.RawMessage `json:"timeUnixNano"`
	Observed       json.RawMessage `json:"observedTimeUnixNano"`
	Body           json.RawMessage `json:"body"`
	Attributes     []attribute     `json:"attributes"`
	Severity       string          `json:"severityText"`
	SeverityNumber int             `json:"severityNumber"`
	TraceID        string          `json:"traceId"`
	SpanID         string          `json:"spanId"`
}

// decoder carries one bounded request and its partial-success accounting.
type decoder struct {
	scope   Scope
	resolve Resolver
	now     time.Time
	records []Record
	result  Result
	count   int
}

// invalid returns a safe error without embedding caller-provided payloads.
func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalid, reason) }

// decode validates the outer signal and all accepted sample records.
func decode(signal string, body []byte, scope Scope, resolve Resolver, now time.Time) ([]Record, Result, error) {
	e, err := decodeEnvelope(signal, body, scope, resolve)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return nil, Result{}, err
	}
	d := decoder{scope: scope, resolve: resolve, now: now}
	// Apply the bounded special case before continuing record processing.
	if signal == "metrics" {
		err = d.metrics(e.ResourceMetrics)
	} else {
		err = d.logs(e.ResourceLogs)
	}
	// Stop on failed validation or I/O before exposing a successful observation.
	if d.result.Rejected > 0 {
		d.result.Message = "Unsupported, unowned or stale observations were rejected"
	}
	return d.records, d.result, err
}

// identity verifies resource metadata before admitting any records from that resource.
func (d *decoder) identity(r resourceAttributes) (Identity, error) {
	attrs, err := attributes(r.Attributes)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return Identity{}, err
	}
	resource := Resource{}
	// Validate each bounded entry before carrying it into the next stage.
	for k, v := range attrs {
		var a map[string]json.RawMessage
		_ = json.Unmarshal(v, &a)
		// Validate each bounded entry before carrying it into the next stage.
		for _, typ := range []string{"stringValue", "intValue", "doubleValue", "boolValue"} {
			value, ok := a[typ]
			// Skip absent values without inventing observations.
			if !ok {
				continue
			}
			var str string
			// Apply the bounded special case before continuing record processing.
			if json.Unmarshal(value, &str) == nil {
				resource[k] = str
			} else {
				resource[k] = string(value)
			}
		}
	}
	id, err := d.resolve(resource)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return Identity{}, err
	}
	// Validate each bounded entry before carrying it into the next stage.
	for _, s := range []string{id.Application, id.Revision, id.Component, id.Pod, id.Node, id.Instance} {
		// Reject identity length before accepting untrusted telemetry.
		if len(s) > 256 {
			return Identity{}, invalid("identity length")
		}
	}
	return id, nil
}

// admit counts rejected and accepted records alike to bound request processing.
func (d *decoder) admit() error {
	d.count++
	// Reject too many records before accepting untrusted telemetry.
	if d.count > MaxRecords {
		return invalid("too many records")
	}
	return nil
}

// add records a fresh observation or reports an explicit partial-success rejection.
func (d *decoder) add(r Record) {
	// Stop on failed validation or I/O before exposing a successful observation.
	if r.ObservedAt.Before(d.now.Add(-Retention)) || r.ObservedAt.After(d.now.Add(5*time.Minute)) {
		d.result.Rejected++
		return
	}
	r.Collector = d.scope.Collector
	r.Runtime = d.scope.Runtime
	r.ReceivedAt = d.now
	d.records = append(d.records, r)
	d.result.Accepted++
}

// timestamp accepts OTLP decimal uint64 timestamps within Go's nanosecond range.
func timestamp(raw json.RawMessage, fallback time.Time) (time.Time, error) {
	// Stop on failed validation or I/O before exposing a successful observation.
	if len(raw) == 0 {
		return fallback, nil
	}
	var s string
	// Apply the bounded special case before continuing record processing.
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	// Reject timestamp before accepting untrusted telemetry.
	if err != nil || n < 0 {
		return time.Time{}, invalid("timestamp")
	}
	// Stop on failed validation or I/O before exposing a successful observation.
	if n == 0 {
		return fallback, nil
	}
	return time.Unix(0, n).UTC(), nil
}

// attributes validates bounded AnyValue attributes and rejects duplicate keys.
func attributes(input []attribute) (map[string]json.RawMessage, error) {
	// Reject too many attributes before accepting untrusted telemetry.
	if len(input) > MaxAttributes {
		return nil, invalid("too many attributes")
	}
	result := make(map[string]json.RawMessage, len(input))
	// Validate each bounded entry before carrying it into the next stage.
	for _, a := range input {
		// Reject attribute key before accepting untrusted telemetry.
		if len(a.Key) == 0 || len(a.Key) > 256 || result[a.Key] != nil {
			return nil, invalid("attribute key")
		}
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := anyValue(a.Value, 0); err != nil {
			return nil, err
		}
		result[a.Key] = a.Value
	}
	return result, nil
}

// anyValue rejects malformed, deeply nested and oversized OTLP values.
func anyValue(raw json.RawMessage, depth int) error {
	// Reject attribute value bounds before accepting untrusted telemetry.
	if depth > 4 || len(raw) > 16384 {
		return invalid("attribute value bounds")
	}
	var v map[string]json.RawMessage
	// Reject AnyValue before accepting untrusted telemetry.
	if json.Unmarshal(raw, &v) != nil || len(v) != 1 {
		return invalid("AnyValue")
	}
	// Validate each bounded entry before carrying it into the next stage.
	for key, value := range v {
		return validateValue(key, value, depth)
	}
	return invalid("AnyValue")
}

// validateValue dispatches the finite standard OTLP AnyValue types.
func validateValue(key string, value json.RawMessage, depth int) error {
	// Dispatch only the explicitly supported OTLP value representations.
	switch key {
	// Validate this OTLP representation using its type-specific bounds.
	case "stringValue", "bytesValue":
		return stringValue(value)
	// Validate this OTLP representation using its type-specific bounds.
	case "intValue":
		return integerValue(value)
	// Validate this OTLP representation using its type-specific bounds.
	case "doubleValue":
		return doubleValue(value)
	// Validate this OTLP representation using its type-specific bounds.
	case "boolValue":
		var b bool
		// Reject boolean value before accepting untrusted telemetry.
		if json.Unmarshal(value, &b) != nil {
			return invalid("boolean value")
		}
	// Validate this OTLP representation using its type-specific bounds.
	case "arrayValue":
		return arrayValue(value, depth)
	// Validate this OTLP representation using its type-specific bounds.
	case "kvlistValue":
		return mapValue(value, depth)
	// Reject unknown representations instead of guessing their meaning.
	default:
		return invalid("unknown AnyValue")
	}
	return nil
}

// stringValue bounds message and attribute payloads in bytes.
func stringValue(value json.RawMessage) error {
	var s string
	// Reject string value before accepting untrusted telemetry.
	if json.Unmarshal(value, &s) != nil || len(s) > 8192 {
		return invalid("string value")
	}
	return nil
}

// integerValue validates lossless signed 64-bit OTLP integer syntax.
func integerValue(value json.RawMessage) error {
	var s string
	// Apply the bounded special case before continuing record processing.
	if json.Unmarshal(value, &s) != nil {
		s = string(value)
	}
	// Reject integer value before accepting untrusted telemetry.
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		return invalid("integer value")
	}
	return nil
}

// doubleValue rejects unsupported nonfinite floating-point observations.
func doubleValue(value json.RawMessage) error {
	var n float64
	// Reject number value before accepting untrusted telemetry.
	if json.Unmarshal(value, &n) != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return invalid("number value")
	}
	return nil
}

// arrayValue bounds nested log and attribute arrays.
func arrayValue(value json.RawMessage, depth int) error {
	var a struct {
		Values []json.RawMessage `json:"values"`
	}
	// Reject array value before accepting untrusted telemetry.
	if json.Unmarshal(value, &a) != nil || len(a.Values) > 64 {
		return invalid("array value")
	}
	// Validate each bounded entry before carrying it into the next stage.
	for _, item := range a.Values {
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := anyValue(item, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// mapValue bounds nested maps without allowing key ambiguity.
func mapValue(value json.RawMessage, depth int) error {
	var a struct {
		Values []attribute `json:"values"`
	}
	// Reject map value before accepting untrusted telemetry.
	if json.Unmarshal(value, &a) != nil || len(a.Values) > 64 {
		return invalid("map value")
	}
	keys := map[string]bool{}
	// Validate each bounded entry before carrying it into the next stage.
	for _, item := range a.Values {
		// Reject map key before accepting untrusted telemetry.
		if len(item.Key) > 256 || keys[item.Key] {
			return invalid("map key")
		}
		keys[item.Key] = true
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := anyValue(item.Value, depth+1); err != nil {
			return err
		}
	}
	return nil
}
