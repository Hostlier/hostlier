package telemetry

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// metrics validates scalar observations and explicitly rejects distribution samples.
func (d *decoder) metrics(groups []resourceMetrics) error {
	// Stop on failed validation or I/O before exposing a successful observation.
	if err := groupCount(len(groups)); err != nil {
		return err
	}
	// Resolve every resource group independently before accepting its samples.
	for _, group := range groups {
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := groupCount(len(group.Scopes)); err != nil {
			return err
		}
		id, err := d.identity(group.Resource)
		// Stop on failed validation or I/O before exposing a successful observation.
		if err != nil {
			// Skip absent values without inventing observations.
			if err = d.metricsIdentityFailure(err, group); err != nil {
				return err
			}
			continue
		}
		// Validate each bounded entry before carrying it into the next stage.
		for _, scope := range group.Scopes {
			// Reject too many metric definitions before accepting untrusted telemetry.
			if len(scope.Metrics) > MaxRecords {
				return invalid("too many metric definitions")
			}
			// Visit each scope observation under the shared request budget.
			for _, metric := range scope.Metrics {
				// Stop on failed validation or I/O before exposing a successful observation.
				if err = d.metric(id, metric); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// metric chooses exactly one OTLP metric type before processing its points.
func (d *decoder) metric(id Identity, m metric) error {
	kind, selected, err := selectMetric(m)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return err
	}
	// Validate each bounded entry before carrying it into the next stage.
	for _, raw := range selected.Data {
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := d.admit(); err != nil {
			return err
		}
		// Skip absent values without inventing observations.
		if kind != "gauge" && kind != "sum" {
			d.result.Rejected++
			continue
		}
		r, err := d.number(id, m, kind, raw)
		// Stop on failed validation or I/O before exposing a successful observation.
		if err != nil {
			return err
		}
		r.Temporality = selected.Temporality
		r.Monotonic = selected.Monotonic
		d.add(r)
	}
	return nil
}

// number decodes finite OTLP gauge/sum values and retains point attributes.
func (d *decoder) number(id Identity, m metric, kind string, raw json.RawMessage) (Record, error) {
	var p numberPoint
	// Reject number point before accepting untrusted telemetry.
	if json.Unmarshal(raw, &p) != nil {
		return Record{}, invalid("number point")
	}
	// Reject metric value before accepting untrusted telemetry.
	if (p.Double == nil) == (len(p.Int) == 0) {
		return Record{}, invalid("metric value")
	}
	value := p.Double
	integerString := ""
	// Apply the bounded special case before continuing record processing.
	if len(p.Int) > 0 {
		var str string
		// Apply the bounded special case before continuing record processing.
		if json.Unmarshal(p.Int, &str) != nil {
			str = string(p.Int)
		}
		integer, err := strconv.ParseInt(str, 10, 64)
		// Reject metric integer before accepting untrusted telemetry.
		if err != nil {
			return Record{}, invalid("metric integer")
		}
		integerString = strconv.FormatInt(integer, 10)
		converted := float64(integer)
		value = &converted
	}
	attrs, err := attributes(p.Attributes)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return Record{}, err
	}
	stamp, err := timestamp(p.Time, d.now)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return Record{}, err
	}
	start, err := timestamp(p.Start, time.Time{})
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return Record{}, err
	}
	return Record{StartTime: start, IntValue: integerString, Signal: "metrics", Identity: id, Name: m.Name, Unit: m.Unit, Kind: kind, Value: value, ObservedAt: stamp, Attributes: attrs}, nil
}

// logs validates complete log groups before their atomic insertion.
func (d *decoder) logs(groups []resourceLogs) error {
	// Stop on failed validation or I/O before exposing a successful observation.
	if err := groupCount(len(groups)); err != nil {
		return err
	}
	// Resolve every resource group independently before accepting its samples.
	for _, group := range groups {
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := groupCount(len(group.Scopes)); err != nil {
			return err
		}
		id, err := d.identity(group.Resource)
		// Stop on failed validation or I/O before exposing a successful observation.
		if err != nil {
			// Skip absent values without inventing observations.
			if err = d.logsIdentityFailure(err, group); err != nil {
				return err
			}
			continue
		}
		// Validate each bounded entry before carrying it into the next stage.
		for _, scope := range group.Scopes {
			// Visit each scope observation under the shared request budget.
			for _, log := range scope.Logs {
				// Stop on failed validation or I/O before exposing a successful observation.
				if err = d.admit(); err != nil {
					return err
				}
				r, err := d.log(id, log)
				// Stop on failed validation or I/O before exposing a successful observation.
				if err != nil {
					return err
				}
				d.add(r)
			}
		}
	}
	return nil
}

// log preserves sensitive log content and correlation fields outside public snapshots.
func (d *decoder) log(id Identity, l logRecord) (Record, error) {
	// Stop on failed validation or I/O before exposing a successful observation.
	if len(l.Body) > 0 {
		// Stop on failed validation or I/O before exposing a successful observation.
		if err := anyValue(l.Body, 0); err != nil {
			return Record{}, err
		}
	}
	attrs, err := attributes(l.Attributes)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return Record{}, err
	}
	observed, err := timestamp(l.Observed, d.now)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return Record{}, err
	}
	stamp, err := timestamp(l.Time, observed)
	// Stop on failed validation or I/O before exposing a successful observation.
	if err != nil {
		return Record{}, err
	}
	// Reject log metadata before accepting untrusted telemetry.
	if !validLogMetadata(l) {
		return Record{}, invalid("log metadata")
	}
	// Validate each bounded entry before carrying it into the next stage.
	for k, v := range map[string]string{"otel.severity_text": l.Severity, "otel.trace_id": l.TraceID, "otel.span_id": l.SpanID} {
		// Apply the bounded special case before continuing record processing.
		if v != "" {
			attrs[k], _ = json.Marshal(map[string]string{"stringValue": v})
		}
	}
	attrs["otel.severity_number"], _ = json.Marshal(map[string]string{"intValue": strconv.Itoa(l.SeverityNumber)})
	return Record{Signal: "logs", Identity: id, ObservedAt: stamp, Kind: "log", Body: l.Body, Attributes: attrs}, nil
}

// rejectMetrics counts foreign-resource points without storing their untrusted identity.
func (d *decoder) rejectMetrics(group resourceMetrics) error {
	// Validate each bounded entry before carrying it into the next stage.
	for _, scope := range group.Scopes {
		// Reject too many metrics before accepting untrusted telemetry.
		if len(scope.Metrics) > MaxRecords {
			return invalid("too many metrics")
		}
		// Visit each scope observation under the shared request budget.
		for _, m := range scope.Metrics {
			// Validate each bounded entry before carrying it into the next stage.
			for _, p := range []*points{m.Gauge, m.Sum, m.Histogram, m.ExponentialHistogram, m.Summary} {
				// Skip absent values without inventing observations.
				if p == nil {
					continue
				}
				// Validate each bounded entry before carrying it into the next stage.
				for range p.Data {
					// Stop on failed validation or I/O before exposing a successful observation.
					if err := d.admit(); err != nil {
						return err
					}
					d.result.Rejected++
				}
			}
		}
	}
	return nil
}

// rejectLogs discards foreign log bodies while accurately reporting partial success.
func (d *decoder) rejectLogs(group resourceLogs) error {
	// Validate each bounded entry before carrying it into the next stage.
	for _, scope := range group.Scopes {
		// Visit each scope observation under the shared request budget.
		for range scope.Logs {
			// Stop on failed validation or I/O before exposing a successful observation.
			if err := d.admit(); err != nil {
				return err
			}
			d.result.Rejected++
		}
	}
	return nil
}

// selectMetric requires one named OTLP data type per metric.
func selectMetric(m metric) (string, *points, error) {
	// Reject metric name or unit before accepting untrusted telemetry.
	if len(m.Name) == 0 || len(m.Name) > 256 || len(m.Unit) > 64 {
		return "", nil, invalid("metric name or unit")
	}
	types := map[string]*points{"gauge": m.Gauge, "sum": m.Sum, "histogram": m.Histogram, "exponentialHistogram": m.ExponentialHistogram, "summary": m.Summary}
	kind := ""
	var selected *points
	// Validate each bounded entry before carrying it into the next stage.
	for k, p := range types {
		// Skip absent values without inventing observations.
		if p == nil {
			continue
		}
		// Reject multiple metric types before accepting untrusted telemetry.
		if selected != nil {
			return "", nil, invalid("multiple metric types")
		}
		kind = k
		selected = p
	}
	// Reject missing metric type before accepting untrusted telemetry.
	if selected == nil {
		return "", nil, invalid("missing metric type")
	}
	return kind, selected, nil
}

// groupCount bounds work even for otherwise empty resource/scope groups.
func groupCount(count int) error {
	// Reject too many groups before accepting untrusted telemetry.
	if count > MaxRecords {
		return invalid("too many groups")
	}
	return nil
}

// metricsIdentityFailure rejects foreign resources but refuses malformed metadata.
func (d *decoder) metricsIdentityFailure(err error, group resourceMetrics) error {
	// Stop on failed validation or I/O before exposing a successful observation.
	if errors.Is(err, ErrInvalid) {
		return err
	}
	return d.rejectMetrics(group)
}

// logsIdentityFailure rejects foreign logs but refuses malformed metadata.
func (d *decoder) logsIdentityFailure(err error, group resourceLogs) error {
	// Stop on failed validation or I/O before exposing a successful observation.
	if errors.Is(err, ErrInvalid) {
		return err
	}
	return d.rejectLogs(group)
}

// validLogMetadata bounds severity and trace correlation metadata.
func validLogMetadata(l logRecord) bool {
	return len(l.Severity) <= 64 && l.SeverityNumber >= 0 && l.SeverityNumber <= 24 && len(l.TraceID) <= 32 && len(l.SpanID) <= 16
}
