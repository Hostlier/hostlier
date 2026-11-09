package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"time"

	"hostlier/internal/model"
	"hostlier/internal/observation"
	"hostlier/internal/store"
)

// statusCapture separates ephemeral observation transport from authoritative controller state.
type statusCapture struct {
	bus               observation.Bus
	mu                sync.Mutex
	demand            time.Time
	wake              chan struct{}
	epoch             string
	sequence          uint64
	streams           int
	interval, timeout time.Duration
}

// startStatusCapture starts exactly one on-demand sampler for all status viewers.
func (c *Controller) startStatusCapture() error {
	interval, timeout := c.opts.StatusInterval, c.opts.StatusTimeout
	// Production capture is deliberately independent of viewer count.
	if interval <= 0 {
		interval = 5 * time.Second
	}
	// One unavailable backend must not monopolize the shared sampler indefinitely.
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	bus, err := observation.NewEmbedded(c.ctx, filepath.Join(c.opts.StateDir, "observations"))
	// Initialization failure must not leave an orphan broker or claim observation readiness.
	if err != nil {
		return err
	}
	c.observations = &statusCapture{bus: bus, wake: make(chan struct{}, 1), epoch: random()[:16], interval: interval, timeout: timeout}
	c.wg.Add(1)
	// Capture belongs to the controller lifecycle, never an individual HTTP request.
	go func() { defer c.wg.Done(); defer bus.Close(); c.statusCaptureLoop() }()
	return nil
}

// touch activates capture while a snapshot reader or stream has recent interest.
func (s *statusCapture) touch() {
	s.mu.Lock()
	cold := time.Since(s.demand) > 15*time.Second
	s.demand = time.Now()
	s.mu.Unlock()
	// Only a cold activation bypasses the regular sampling interval.
	if cold {
		// Coalescing bounds wakeups from simultaneous clients.
		select {
		// Wake the one shared worker.
		case s.wake <- struct{}{}:
		// A pending wake already requests capture.
		default:
		}
	}
}

// wanted avoids collecting diagnostics when nobody is observing deployments.
func (s *statusCapture) wanted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.demand) < 15*time.Second
}

// statusCaptureLoop collects on activation and on a fixed shared cadence.
func (c *Controller) statusCaptureLoop() {
	s := c.observations
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	// A single loop prevents overlapping captures even when the runtime is slow.
	for {
		// Shutdown interrupts both waiting and runtime inspection contexts.
		select {
		// The controller owns this worker's lifetime.
		case <-c.ctx.Done():
			return
		// Cold readers request a first capture immediately.
		case <-s.wake:
		// Active readers share the periodic capture.
		case <-ticker.C:
		}
		// Idle controllers incur no runtime sampling work.
		if s.wanted() {
			c.captureStatus()
		}
	}
}

// statusMetadata copies only public scalar metadata while briefly holding authoritative state.
func (c *Controller) statusMetadata() (model.StatusSnapshot, error) {
	c.mu.Lock()
	out := model.StatusSnapshot{Hostname: c.state.Hostname, Runtime: c.state.Runtime, Generation: c.state.Generation, Applications: []model.ApplicationHealth{}, Operations: []model.OperationHealth{}}
	apps, err := collectionValues[*Application](c.ctx, c.store, "applications", store.CollectionQuery{ExcludeStatus: "deleted", Limit: 129})
	if err != nil {
		c.mu.Unlock()
		return out, err
	}
	operations, err := collectionValues[*Operation](c.ctx, c.store, "operations", store.CollectionQuery{ActiveFirst: true, Limit: 129})
	if err != nil {
		c.mu.Unlock()
		return out, err
	}
	// Configuration, environment, credentials and workflow outputs never enter the observation bus.
	for _, app := range apps {
		out.Applications = append(out.Applications, model.ApplicationHealth{Name: app.Name, URL: app.URL, Status: app.Status, Revision: app.Revision, DesiredReplicas: model.DesiredReplicas(app.Spec), Reason: "Observation pending"})
	}
	// Operation summaries retain useful receipts without serializing deployment specifications.
	for _, op := range operations {
		out.Operations = append(out.Operations, model.OperationHealth{ID: op.ID, Name: op.Name, Kind: op.Kind, Status: op.Status})
	}
	c.mu.Unlock()
	out.Telemetry = c.telemetryHealth()
	return boundStatus(out), nil
}

// boundStatus gives replacement snapshots stable order and a bounded inventory size.
func boundStatus(out model.StatusSnapshot) model.StatusSnapshot {
	// The explicit marker tells clients when to use ordinary inventory APIs for the remainder.
	if len(out.Applications) > 128 {
		out.Applications = out.Applications[:128]
		out.Truncated = true
	}
	// Bound long-lived controller operation history in every streamed frame.
	if len(out.Operations) > 128 {
		out.Operations = out.Operations[:128]
		out.Truncated = true
	}
	return out
}

// captureStatus performs runtime I/O without holding controller state or subscriber locks.
func (c *Controller) captureStatus() {
	s := c.observations
	out, err := c.statusMetadata()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	// Each application is sampled once per cycle, regardless of the number of consumers.
	c.captureApplications(ctx, out.Applications)
	s.sequence++
	out.ID = fmt.Sprintf("%s:%d", s.epoch, s.sequence)
	out.ObservedAt = time.Now().UTC()
	data, err := json.Marshal(out)
	// Serialization failure cannot replace the last usable observation.
	if err == nil {
		s.bus.Publish(string(data))
	}
}

// captureApplications bounds runtime concurrency independently of viewers and application count.
func (c *Controller) captureApplications(ctx context.Context, apps []model.ApplicationHealth) {
	// A controller without applications needs no worker pool or modulo arithmetic.
	if len(apps) == 0 {
		return
	}
	jobs := make(chan int, len(apps))
	start := int(c.observations.sequence * 4 % uint64(len(apps)))
	// Rotate admission so repeated cycle deadlines cannot permanently starve later applications.
	for i := range apps {
		jobs <- (start + i) % len(apps)
	}
	close(jobs)
	var workers sync.WaitGroup
	// Four workers bound pressure on Docker/k3s while isolating unrelated observations.
	for range min(4, len(apps)) {
		workers.Add(1)
		// Each index has one writer and workers finish before snapshot serialization.
		go func() {
			defer workers.Done()
			// A canceled cycle marks remaining jobs unavailable without starting further runtime I/O.
			for i := range jobs {
				c.observeApplication(ctx, &apps[i])
			}
		}()
	}
	workers.Wait()
}

// observeApplication collects bounded, allowlisted runtime fields and numeric usage.
func (c *Controller) observeApplication(parent context.Context, app *model.ApplicationHealth) {
	// Deleted workloads and an exhausted cycle have no new runtime observation.
	if app.Status == "deleted" || parent.Err() != nil {
		app.Reason = "Runtime observation unavailable"
		return
	}
	ctx, cancel := context.WithTimeout(parent, c.observations.timeout)
	defer cancel()
	inspector, ok := c.opts.Runtime.(runtimeInspector)
	// Optional runtimes must report absence instead of inventing health.
	if !ok {
		app.Reason = "Runtime does not provide status observations"
		return
	}
	diagnostics, err := inspector.Status(ctx, app.Name)
	// Never put raw provider errors onto the bus or into browser-visible state.
	if err != nil {
		app.Reason = "Runtime observation unavailable"
		return
	}
	app.Available = true
	app.Reason = ""
	app.ObservedAt = time.Now().UTC()
	app.Running, _ = diagnostics["running"].(bool)
	// A retained sleep is healthy at zero desired replicas.
	if sleeping, _ := diagnostics["idleSleeping"].(bool); sleeping {
		app.DesiredReplicas = 0
		app.Status = "sleeping"
	}
	app.RestartCount, _ = diagnostics["restartCount"].(int)
	app.OOMKilled, _ = diagnostics["oomKilled"].(bool)
	// Readiness is explicitly unknown when the adapter supplies no replica measurement.
	if ready, ok := diagnosticCount(diagnostics["readyReplicas"]); ok {
		app.ReadyReplicas = &ready
		app.Running = app.Running || ready > 0
	}
	c.observeComponents(app, diagnostics)
	c.observeUsage(ctx, app)
}

// diagnosticCount normalizes native Go counters and JSON-decoded Kubernetes numbers.
func diagnosticCount(value any) (int, bool) {
	// Backend maps may originate from typed Docker data or JSON Kubernetes responses.
	switch number := value.(type) {
	// Typed adapters already provide integral counters.
	case int:
		return number, number >= 0
	// JSON numbers must be finite, bounded and integral before conversion.
	case float64:
		return int(number), number >= 0 && number <= 1000000 && math.Trunc(number) == number
	}
	return 0, false
}

// observeComponents derives process state from allowlisted Docker and Kubernetes component records.
func (c *Controller) observeComponents(app *model.ApplicationHealth, diagnostics map[string]any) {
	components, ok := diagnostics["components"].([]model.ComponentStatus)
	// A backend with no component observations keeps its original unknown readiness marker.
	if !ok || len(components) == 0 {
		return
	}
	ready := 0
	// Only bounded typed process fields enter the public DTO, never raw probe output.
	for _, component := range components {
		app.Running = app.Running || component.Running
		app.OOMKilled = app.OOMKilled || component.OOMKilled
		app.RestartCount += component.RestartCount
		// Primary components define application replica readiness; sidecar state remains diagnostic.
		if component.Name == "app" && component.Ready {
			ready++
		}
	}
	// Preserve authoritative deployment replica counts when Kubernetes supplies them.
	if app.ReadyReplicas == nil {
		app.ReadyReplicas = &ready
	}
}

// observeUsage copies numeric measurements only; free-form backend text is excluded.
func (c *Controller) observeUsage(ctx context.Context, app *model.ApplicationHealth) {
	observer, ok := c.opts.Runtime.(model.UsageRuntime)
	// Status remains useful even for runtimes without usage measurements.
	if !ok {
		app.Usage = &model.Usage{Source: c.opts.Runtime.Name(), Reason: "Runtime does not provide resource measurements"}
		return
	}
	usage, err := observer.Usage(ctx, app.Name)
	// A failed metric probe must not erase a successful runtime status observation.
	if err != nil {
		app.Usage = &model.Usage{Source: c.opts.Runtime.Name(), Reason: "Resource measurements unavailable"}
		// Deadline status is safe diagnostic metadata; raw runtime errors must never enter the shared stream.
		if ctx.Err() != nil {
			app.Usage.Reason = "Resource measurement timed out"
		}
		return
	}
	reason := statusUsageReason(usage)
	// Adapters may return a partial result rather than an error when their probe deadline expires.
	if !usage.Available && ctx.Err() != nil {
		reason = "Resource measurement timed out"
	}
	app.Usage = &model.Usage{Reason: reason, Available: usage.Available, Source: c.opts.Runtime.Name(), CPUCores: usage.CPUCores, MemoryBytes: usage.MemoryBytes, ObservedAt: usage.ObservedAt, DesiredReplicas: app.DesiredReplicas, ReadyReplicas: usage.ReadyReplicas}
}

// statusUsageReason describes missing numeric measurements without publishing arbitrary backend diagnostics.
func statusUsageReason(usage model.Usage) string {
	// A complete numeric observation needs no warning.
	if usage.Available {
		return ""
	}
	// Independent memory samples remain useful while the first CPU delta warms up.
	if usage.MemoryBytes != nil && usage.CPUCores == nil {
		return "CPU sampling is warming up; memory is available"
	}
	return "Resource measurements unavailable"
}
