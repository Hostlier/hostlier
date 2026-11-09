package runtime

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"time"
)

// autoscalingMetricsReady verifies an available, fresh metrics-server API without computing CPU decisions.
func (k *K3s) autoscalingMetricsReady(ctx context.Context) error {
	raw, err := k.run(ctx, []string{"get", "--raw", "/apis/metrics.k8s.io/v1beta1/nodes"}, nil)
	// An unavailable metrics API cannot authorize initial delegation.
	if err != nil {
		return model.Err("AUTOSCALING_METRICS", "Autoscaling requires a healthy metrics-server")
	}
	var response struct {
		Items []struct {
			Timestamp time.Time `json:"timestamp"`
		} `json:"items"`
	}
	// Require at least one current node sample rather than a merely registered API group.
	if json.Unmarshal(raw, &response) != nil || len(response.Items) == 0 {
		return model.Err("AUTOSCALING_METRICS", "Metrics-server has no current samples")
	}
	// Every reported node sample must establish current collection health.
	for _, item := range response.Items {
		age := time.Since(item.Timestamp)
		// Reject stale samples and timestamps materially ahead of this host's clock.
		if age > 2*time.Minute || age < -30*time.Second {
			return model.Err("AUTOSCALING_METRICS", "Metrics-server samples are stale")
		}
	}
	return nil
}

// observeAutoscalingMetrics persists outage onset and revokes after five controller-observed minutes.
func (k *K3s) observeAutoscalingMetrics(ctx context.Context, s state, app appRecord) error {
	a := app.Autoscaling
	// Kubernetes retains its documented missing-metric behavior during the short outage grace period.
	if err := k.autoscalingMetricsReady(ctx); err != nil {
		// Persist the first failed observation before starting the outage grace period.
		if a.MetricsUnavailableSince == 0 {
			a.MetricsUnavailableSince = time.Now().Unix()
			s.Apps[app.Name] = app
			return k.base.save(s)
		}
		// This guard runs only while Hostlier is available; it is not an expiry guarantee.
		if time.Since(time.Unix(a.MetricsUnavailableSince, 0)) >= 5*time.Minute {
			return k.pauseAutoscaling(ctx, s, app.Name, "metrics API unavailable for five observed minutes")
		}
		return nil
	}
	// Restored collection clears the outage marker without changing replicas.
	if a.MetricsUnavailableSince != 0 {
		a.MetricsUnavailableSince = 0
		s.Apps[app.Name] = app
		return k.base.save(s)
	}
	return nil
}

// foreignReplicaWriter detects explicit scale ownership changes and manually stopped capacity.
func foreignReplicaWriter(object map[string]any) bool {
	spec, _ := object["spec"].(map[string]any)
	// Native positive-minimum HPA never requests zero; an external stop takes precedence.
	if replicas, ok := spec["replicas"].(float64); ok && replicas == 0 {
		return true
	}
	metadata, _ := object["metadata"].(map[string]any)
	fields, _ := metadata["managedFields"].([]any)
	latest := time.Time{}
	manager := ""
	// Consider managers owning replicas, excluding unrelated status writers.
	for _, field := range fields {
		entry, _ := field.(map[string]any)
		fieldSet, _ := entry["fieldsV1"].(map[string]any)
		fieldSpec, _ := fieldSet["f:spec"].(map[string]any)
		// Ignore status, pod templates and managers which do not own desired replicas.
		if _, ok := fieldSpec["f:replicas"]; !ok {
			continue
		}
		timestamp, _ := entry["time"].(string)
		stamp, err := time.Parse(time.RFC3339, timestamp)
		// Use the most recent valid Kubernetes manager timestamp.
		if err == nil && !stamp.Before(latest) {
			latest = stamp
			manager, _ = entry["manager"].(string)
		}
	}
	return !nativeReplicaManager(manager)
}

// nativeReplicaManager recognizes only Hostlier and the pinned Kubernetes controller identities.
func nativeReplicaManager(manager string) bool {
	return manager == "" || manager == "hostlier" || manager == "k3s" || manager == "kube-controller-manager" || manager == "horizontal-pod-autoscaler"
}

// autoscalerReplicaAgreement observes desired-count drift without computing native HPA CPU decisions.
func (k *K3s) autoscalerReplicaAgreement(ctx context.Context, app appRecord, hpa map[string]any) (bool, error) {
	target, err := k.get(ctx, "deployment", app.RuntimeID, k.namespace(app.Name))
	// Failed cluster observation cannot establish agreement or authorize replacement.
	if err != nil {
		return false, err
	}
	// Native HPA updates scale and status separately; a brief verification avoids that normal write race.
	if hpaReplicaAgreement(target, hpa) {
		return true, nil
	}
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	// Shutdown wins over delayed ownership reobservation.
	select {
	// Shutdown or the bounded observation timer controls the next fixture step.
	case <-ctx.Done():
		return false, ctx.Err() // Cancellation leaves ownership undecided.
	case <-timer.C: // Reobserve the two native fields after the short write propagation window.
	}
	target, err = k.get(ctx, "deployment", app.RuntimeID, k.namespace(app.Name))
	// An unreadable authoritative observation must not authorize mutation.
	if err != nil {
		return false, err
	}
	observed, err := k.get(ctx, "horizontalpodautoscaler", "autoscaler", k.namespace(app.Name))
	// An unreadable authoritative observation must not authorize mutation.
	if err != nil {
		return false, err
	}
	return hpaReplicaAgreement(target, observed), nil
}

// hpaReplicaAgreement accepts unsynchronized initial HPA status but detects visible manual count drift.
func hpaReplicaAgreement(target, hpa map[string]any) bool {
	status, _ := hpa["status"].(map[string]any)
	desired, known := status["desiredReplicas"].(float64)
	// A freshly installed HPA has no authoritative status count until its first native sync.
	if !known || desired < 1 {
		return true
	}
	spec, _ := target["spec"].(map[string]any)
	actual, known := spec["replicas"].(float64)
	return known && actual == desired
}
