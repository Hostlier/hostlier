package model

import "time"

// StatusSnapshot is a replaceable observation, independent of durable deployment state.
type StatusSnapshot struct {
	Telemetry    *TelemetryHealth    `json:"telemetry,omitempty"`
	ID           string              `json:"id"`
	Hostname     string              `json:"hostname"`
	Runtime      string              `json:"runtime"`
	Generation   int64               `json:"generation"`
	ObservedAt   time.Time           `json:"observedAt"`
	Applications []ApplicationHealth `json:"applications"`
	Operations   []OperationHealth   `json:"operations"`
	Truncated    bool                `json:"truncated"`
}

// TelemetryHealth summarizes ingestion without copying sensitive log records into public observations.
type TelemetryHealth struct {
	Managed        *ManagedCollectorHealth `json:"managed,omitempty"`
	Collectors     int                     `json:"collectors"`
	Accepted       int64                   `json:"accepted"`
	Rejected       int64                   `json:"rejected"`
	LastReceivedAt time.Time               `json:"lastReceivedAt,omitempty"`
	Fresh          bool                    `json:"fresh"`
}

// ApplicationHealth intentionally excludes configuration, environment and secret references.
type ApplicationHealth struct {
	Name            string            `json:"name"`
	URL             string            `json:"url,omitempty"`
	Status          string            `json:"status"`
	Revision        string            `json:"revision,omitempty"`
	DesiredReplicas int               `json:"desiredReplicas"`
	ObservedAt      time.Time         `json:"observedAt,omitempty"`
	Available       bool              `json:"available"`
	Reason          string            `json:"reason,omitempty"`
	Running         bool              `json:"running"`
	ReadyReplicas   *int              `json:"readyReplicas,omitempty"`
	RestartCount    int               `json:"restartCount"`
	OOMKilled       bool              `json:"oomKilled"`
	Components      []ComponentStatus `json:"components,omitempty"`
	Usage           *Usage            `json:"usage,omitempty"`
}

// OperationHealth is a public receipt without deployment inputs or private workflow outputs.
type OperationHealth struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// ManagedCollectorHealth describes shared collector supervision independently from app readiness.
type ManagedCollectorHealth struct {
	State         string    `json:"state"`
	Reason        string    `json:"reason,omitempty"`
	LastAttemptAt time.Time `json:"lastAttemptAt,omitempty"`
	LastReadyAt   time.Time `json:"lastReadyAt,omitempty"`
}
