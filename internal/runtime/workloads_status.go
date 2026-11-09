package runtime

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
)

// dockerComponents exposes only typed health state for the primary and owned sidecars.
func (r *Runtime) dockerComponents(ctx context.Context, app appRecord) []model.ComponentStatus {
	ids := map[string]string{"app": app.RuntimeID}
	// Every member is selected from the authoritative group journal.
	for name, id := range app.Members {
		ids[name] = id
	}
	out := []model.ComponentStatus{}
	// Inspection never includes container configuration or healthcheck output.
	for name, id := range ids {
		item := model.ComponentStatus{Name: name}
		c, err := r.container(ctx, id, app.Name)
		// An unreadable or missing process stays explicitly unready.
		if err == nil {
			item.Running = c.State.Running
			item.Ready = c.State.Running && c.State.Health.Status != "unhealthy"
			item.ExitCode = c.State.ExitCode
			item.OOMKilled = c.State.OOMKilled
		}
		out = append(out, item)
	}
	return out
}

// podState decodes only health fields so backend diagnostics cannot expose secret manifests.
type podState struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Containers []podComponent `json:"containerStatuses"`
			Sidecars   []podComponent `json:"initContainerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

// podComponent retains the typed Kubernetes process health fields used in component status.
type podComponent struct {
	Name         string `json:"name"`
	Ready        bool   `json:"ready"`
	RestartCount int    `json:"restartCount"`
	State        struct {
		Running    *struct{} `json:"running"`
		Terminated *struct {
			ExitCode int    `json:"exitCode"`
			Reason   string `json:"reason"`
		} `json:"terminated"`
	} `json:"state"`
}

// k3sComponents reports each replica's primary and native sidecar health independently.
func (k *K3s) k3sComponents(ctx context.Context, app appRecord) []model.ComponentStatus {
	raw, err := k.run(ctx, []string{"get", "pods", "-n", k.namespace(app.Name), "-l", "io.hostlier.version=" + app.RuntimeID, "-o", "json"}, nil)
	// Failed inspection cannot invent ready processes.
	if err != nil {
		return nil
	}
	var pods podState
	// Malformed cluster responses must not escape the typed status boundary.
	if json.Unmarshal(raw, &pods) != nil {
		return nil
	}
	out := []model.ComponentStatus{}
	// A scaled group has one diagnostic row per component per Pod.
	for _, pod := range pods.Items {
		// Native sidecars appear under initContainerStatuses in Kubernetes.
		for _, component := range append(pod.Status.Containers, pod.Status.Sidecars...) {
			out = append(out, component.status(pod.Metadata.Name))
		}
	}
	return out
}

// status converts one Kubernetes state to the backend-independent safe diagnostic shape.
func (c podComponent) status(pod string) model.ComponentStatus {
	item := model.ComponentStatus{Name: c.Name, Instance: pod, Ready: c.Ready, Running: c.State.Running != nil, RestartCount: c.RestartCount}
	// Terminated process metadata is useful without exposing its arbitrary termination message.
	if c.State.Terminated != nil {
		item.ExitCode = c.State.Terminated.ExitCode
		item.OOMKilled = c.State.Terminated.Reason == "OOMKilled"
	}
	return item
}
