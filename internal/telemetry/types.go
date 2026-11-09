// Package telemetry accepts bounded OTLP JSON observations independently of deployment state.
package telemetry

import (
	"context"
	"encoding/json"
	"time"
)

// Scope identifies authenticated collector authority supplied by the controller.
type Scope struct {
	Collector string `json:"collector"`
	Runtime   string `json:"runtime"`
}

// Resource contains untrusted scalar OTLP resource attributes for ownership resolution.
type Resource map[string]string

// Identity is authoritative attribution returned by a controller-owned resolver.
type Identity struct {
	Application string `json:"application,omitempty"`
	Revision    string `json:"revision,omitempty"`
	Component   string `json:"component,omitempty"`
	Pod         string `json:"pod,omitempty"`
	Node        string `json:"node,omitempty"`
	Instance    string `json:"instance,omitempty"`
}

// Resolver must verify ownership rather than trust claimed resource attributes.
type Resolver func(Resource) (Identity, error)

// Record is a sensitive historical observation, never deployment authority.
type Record struct {
	ID          int64                      `json:"id"`
	Signal      string                     `json:"signal"`
	Collector   string                     `json:"collector"`
	Runtime     string                     `json:"runtime"`
	Identity    Identity                   `json:"identity"`
	ObservedAt  time.Time                  `json:"observedAt"`
	ReceivedAt  time.Time                  `json:"receivedAt"`
	Name        string                     `json:"name,omitempty"`
	Unit        string                     `json:"unit,omitempty"`
	Kind        string                     `json:"kind,omitempty"`
	StartTime   time.Time                  `json:"startTime,omitempty"`
	Temporality int                        `json:"temporality,omitempty"`
	Monotonic   bool                       `json:"monotonic,omitempty"`
	IntValue    string                     `json:"intValue,omitempty"`
	Value       *float64                   `json:"value,omitempty"`
	Body        json.RawMessage            `json:"body,omitempty"`
	Attributes  map[string]json.RawMessage `json:"attributes,omitempty"`
}

// Result reports OTLP partial success without exposing rejected payloads.
type Result struct {
	Accepted int    `json:"accepted"`
	Rejected int    `json:"rejected"`
	Message  string `json:"message,omitempty"`
}

// Query bounds reverse chronological history within an optional workload identity.
type Query struct {
	Signal, Application, Revision, Component string
	Limit                                    int
	Before                                   int64
	From, To, BeforeTime                     time.Time
	Search, Level                            string
}

// Page provides bounded history and an exclusive cursor for older entries.
type Page struct {
	Records        []Record `json:"records"`
	NextBeforeTime string   `json:"nextBeforeTime,omitempty"`
	RetentionDays  int      `json:"retentionDays"`
	RetainedFrom   string   `json:"retainedFrom,omitempty"`
	NextBefore     int64    `json:"nextBefore,omitempty"`
}

// Limits keep ingress, history and caller allocation finite.
const (
	MaxBodyBytes      = 1 << 20
	MaxRecords        = 512
	MaxAttributes     = 64
	MaxHistoryRecords = 100000
	MaxHistoryBytes   = 128 << 20
	MaxQueryRecords   = 200
	MaxRecordBytes    = 64 << 10
	MaxQueryBytes     = 2 << 20
	Retention         = 30 * 24 * time.Hour
)

// Ingest validates an entire request and resolves ownership before any storage writes.
func (s *Store) Ingest(ctx context.Context, signal string, body []byte, scope Scope, resolve Resolver) (Result, error) {
	// Stop on failed validation or I/O before exposing a successful observation.
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	records, result, err := decode(signal, body, scope, resolve, s.now())
	// Stop on failed validation or I/O before exposing a successful observation.
	if cancelErr := ctx.Err(); cancelErr != nil {
		return Result{}, cancelErr
	}
	// Invalid requests must not partially write accepted observations.
	if err != nil {
		return Result{}, err
	}
	// Persist accepted observations under independent bounded retention.
	if err = s.append(ctx, records); err != nil {
		return Result{}, err
	}
	return result, nil
}
