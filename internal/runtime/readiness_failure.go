package runtime

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
)

// containerExitError distinguishes observed kernel memory termination from an ordinary process exit.
func containerExitError(c dockerContainer) error {
	// Report only the daemon's explicit memory-kill observation, never an inferred timeout.
	if c.State.OOMKilled {
		return model.Err("APPLICATION_OOM", "Application exceeded its memory limit before readiness")
	}
	return model.Err("APPLICATION_EXIT", "Application exited before readiness")
}

// rolloutFailure preserves the original error unless owned candidate pods prove a specific failure.
func (k *K3s) rolloutFailure(ctx context.Context, namespace, version string, original error) error {
	data, err := k.run(ctx, []string{"get", "pods", "-n", namespace, "-l", "io.hostlier.version=" + version, "-o", "json"}, nil)
	// Diagnostics are best effort and cannot replace the failed rollout with success.
	if err != nil {
		return original
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	// Invalid observations retain the original failure without revealing raw cluster output.
	if json.Unmarshal(data, &list) != nil {
		return original
	}
	// Only server-owned candidate pods can establish a user-facing diagnosis.
	for _, pod := range list.Items {
		// Do not trust injected pods merely because they match a version selector.
		if !k.owned(pod) {
			continue
		}
		// Return a fixed diagnostic message rather than arbitrary container or scheduler output.
		if err = podReadinessFailure(pod); err != nil {
			return err
		}
	}
	return original
}

// podReadinessFailure recognizes bounded scheduler and container termination reasons.
func podReadinessFailure(pod map[string]any) error {
	status, _ := pod["status"].(map[string]any)
	conditions, _ := status["conditions"].([]any)
	// Explicit unschedulability is distinct from a merely slow health check.
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		// PodScheduled false with the standard reason is authoritative scheduling evidence.
		if condition["type"] == "PodScheduled" && condition["status"] == "False" && condition["reason"] == "Unschedulable" {
			return model.Err("UNSCHEDULABLE", "Application cannot be scheduled with its requested resources and placement")
		}
	}
	statuses, _ := status["containerStatuses"].([]any)
	// Both current and last termination state survive kubelet retry behavior.
	for _, raw := range statuses {
		container, _ := raw.(map[string]any)
		// A real OOM event is more specific than a generic rollout timeout.
		if terminatedForOOM(container["state"]) || terminatedForOOM(container["lastState"]) {
			return model.Err("APPLICATION_OOM", "Application exceeded its memory limit before readiness")
		}
	}
	return nil
}

// terminatedForOOM extracts only the standard Kubernetes memory-termination reason.
func terminatedForOOM(value any) bool {
	state, _ := value.(map[string]any)
	terminated, _ := state["terminated"].(map[string]any)
	return terminated["reason"] == "OOMKilled"
}

// rolloutReadinessError retains specific observed failures while normalizing generic rollout errors.
func rolloutReadinessError(err error) error {
	// These fixed runtime diagnoses carry no raw process or scheduler output.
	if hasCode(err, "UNSCHEDULABLE") || hasCode(err, "APPLICATION_OOM") {
		return err
	}
	return model.Err("HEALTH_TIMEOUT", "Kubernetes workload did not become HTTP ready")
}
