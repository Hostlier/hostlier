package model

import "context"

// TelemetryIdentity is workload attribution established by runtime ownership, never caller labels.
type TelemetryIdentity struct {
	Application string
	// Revision identifies the runtime deployment; the controller maps its own retained revision.
	Revision string
	// Component is empty for a verified Pod aggregate rather than inventing a primary-container value.
	Component string
	// Instance is a full Docker container ID or Kubernetes Pod UID.
	Instance string
}

// TelemetryRuntime resolves collector resource coordinates against current owned workloads.
type TelemetryRuntime interface {
	// ResolveTelemetry rejects stale, unknown, incomplete, and foreign workload coordinates.
	ResolveTelemetry(context.Context, map[string]string) (TelemetryIdentity, error)
}
