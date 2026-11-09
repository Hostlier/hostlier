package controller

import (
	"hostlier/internal/config"
	"hostlier/internal/model"
	"strconv"
)

// aggregateCapacity expresses approved app limits across the desired replica count.
func aggregateCapacity(spec model.Project, replicas int) map[string]any {
	capacity := map[string]any{"replicas": replicas}
	// Missing per-container limits remain explicitly unspecified in the preview.
	if spec.Resources == nil {
		return capacity
	}
	// Validated CPU quantities have shared millicore precision across runtimes.
	if spec.Resources.CPU != "" {
		cpu, err := strconv.ParseFloat(spec.Resources.CPU, 64)
		// Only validated quantities contribute a concrete aggregate.
		if err == nil {
			capacity["cpuCores"] = cpu * float64(replicas)
		}
	}
	// Byte totals avoid ambiguity when limits use different memory quantity suffixes.
	if spec.Resources.Memory != "" {
		memory, err := config.ParseMemory(spec.Resources.Memory)
		// Only validated quantities contribute a concrete aggregate.
		if err == nil {
			capacity["memoryBytes"] = float64(memory) * float64(replicas)
		}
	}
	return capacity
}
