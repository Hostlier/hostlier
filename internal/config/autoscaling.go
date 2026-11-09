package config

import (
	"hostlier/internal/model"
	"strconv"
)

// validateProjectAutoscaling requires explicit, bounded requests and stateless v1 workloads.
func validateProjectAutoscaling(p model.Project) (model.Project, error) {
	// Requests must be valid even without delegation.
	if p.Requests != nil {
		// Workload groups do not yet implement explicit application-container requests.
		if p.Version != 1 {
			return p, invalid("Explicit resource requests currently require version 1")
		}
		// Invalid request quantities fail before deployment planning.
		if err := validateRequests(p); err != nil {
			return p, err
		}
	}
	a := p.Autoscaling
	// Omitted delegation preserves ordinary manual replica ownership.
	if a == nil {
		return p, nil
	}
	// Exclude zero, stateful storage, independent workloads and unbounded allocations.
	if !autoscalingWorkload(p) {
		return p, invalid("Autoscaling requires stateless version 1 with explicit requests and limits")
	}
	// All automatic capacity stays inside the reviewed positive interval.
	if !autoscalingBounds(a) {
		return p, invalid("Autoscaling requires 1 <= minReplicas <= maxReplicas <= 100 and CPU target 1 to 100")
	}
	// Initial/manual intent cannot silently wake zero or exceed delegated bounds.
	if count := model.DesiredReplicas(p); count < a.MinReplicas || count > a.MaxReplicas {
		return p, invalid("Initial replicas must be within autoscaling bounds")
	}
	return p, nil
}

// validateRequests checks request quantities and their relationship to explicit limits.
func validateRequests(p model.Project) error {
	request := p
	request.Resources = p.Requests
	// Reuse canonical quantity validation before comparing allocations.
	if _, err := validateProjectResources(request); err != nil {
		return err
	}
	// Both denominators and memory scheduling must be explicit for delegated workloads.
	if !completeAllocation(p.Requests) || !completeAllocation(p.Resources) {
		return invalid("Requests require explicit CPU and memory requests and limits")
	}
	cpu, _ := strconv.ParseFloat(p.Requests.CPU, 64)
	limit, _ := strconv.ParseFloat(p.Resources.CPU, 64)
	memory, _ := ParseMemory(p.Requests.Memory)
	maximum, _ := ParseMemory(p.Resources.Memory)
	// Scheduling requests cannot exceed runtime resource limits.
	if cpu > limit || memory > maximum {
		return invalid("Requests must not exceed limits")
	}
	return nil
}

// autoscalingWorkload confines delegation to one explicitly allocated stateless v1 container.
func autoscalingWorkload(p model.Project) bool {
	return p.Version == 1 && len(p.Sidecars) == 0 && !model.HasVolumes(p) && p.Requests != nil && p.Resources != nil
}

// autoscalingBounds limits the reviewed positive capacity range and CPU request percentage.
func autoscalingBounds(a *model.Autoscaling) bool {
	return a.MinReplicas >= 1 && a.MaxReplicas >= a.MinReplicas && a.MaxReplicas <= 100 && a.CPUTargetPercent >= 1 && a.CPUTargetPercent <= 100
}

// completeAllocation requires both resource dimensions for deterministic scheduling and metrics.
func completeAllocation(a *model.Limits) bool { return a != nil && a.CPU != "" && a.Memory != "" }
