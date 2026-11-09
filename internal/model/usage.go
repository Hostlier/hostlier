package model

import (
	"context"
	"time"
)

// Usage reports an owned workload's observed measurements separately from approved desired settings.
type Usage struct {
	Available        bool      `json:"available"`
	Reason           string    `json:"reason,omitempty"`
	Source           string    `json:"source"`
	CPUCores         *float64  `json:"cpuCores,omitempty"`
	MemoryBytes      *int64    `json:"memoryBytes,omitempty"`
	ObservedAt       time.Time `json:"observedAt,omitempty"`
	Limits           *Limits   `json:"limits,omitempty"`
	CPUCapacityCores float64   `json:"cpuCapacityCores,omitempty"`
	MemoryLimitBytes int64     `json:"memoryLimitBytes,omitempty"`
	DesiredReplicas  int       `json:"desiredReplicas"`
	ReadyReplicas    int       `json:"readyReplicas"`
	Status           string    `json:"status"`
}

// UsageRuntime is an optional authenticated read-only extension for runtime metrics.
type UsageRuntime interface {
	// Usage observes actual resource consumption and current desired/ready state.
	Usage(context.Context, string) (Usage, error)
}
