package controller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"hostlier/internal/collector"
	"hostlier/internal/collectorimage"
	"hostlier/internal/model"
)

// managedCollectorService supervises one runtime collector without application locks.
type managedCollectorService struct {
	mu      sync.RWMutex
	image   *collectorimage.Image
	health  model.ManagedCollectorHealth
	started bool
}

// startManagedCollector starts only after listeners exist; caller holds the controller lock.
func (c *Controller) startManagedCollector(o ListenOptions) {
	// Stop before publishing authority or readiness when the prerequisite fails.
	if !c.opts.ManagedCollector || c.managed.started {
		return
	}
	c.managed.mu.Lock()
	defer c.managed.mu.Unlock()
	c.managed.started = true
	c.managed.health = model.ManagedCollectorHealth{State: "starting"}
	// Stop before publishing authority or readiness when the prerequisite fails.

	c.wg.Add(1)
	go c.runManagedCollector()
}

// runManagedCollector bounds every attempt and waits between repair attempts.
func (c *Controller) runManagedCollector() {
	defer c.wg.Done()
	interval := c.opts.CollectorInterval
	// Handle this lifecycle state before continuing bounded reconciliation.
	if interval <= 0 {
		interval = time.Minute
	}
	// Process the bounded credential inventory or wait between supervision attempts.
	for {
		ctx, cancel := context.WithTimeout(c.ctx, 45*time.Second)
		err := c.reconcileReadyManagedCollector(ctx)
		cancel()
		c.recordManagedCollector(err)
		delay := interval
		// HTTPS bootstrap is a prerequisite wait, not a failed runtime installation.
		if errors.Is(err, errCollectorHTTPSPending) {
			delay = min(interval, time.Second)
		}
		timer := time.NewTimer(delay)
		// Stop supervision promptly when controller shutdown cancels its context.
		select {
		// Resume only for the selected lifecycle signal.
		case <-c.ctx.Done():
			timer.Stop()
			return
		// Resume only for the selected lifecycle signal.
		case <-timer.C:
		}
	}
}

// reconcileManagedCollector publishes pull authority before invoking runtime supervision.
func (c *Controller) reconcileManagedCollector(ctx context.Context) error {
	runtime, ok := c.opts.Runtime.(model.ManagedCollectorRuntime)
	// Stop before publishing authority or readiness when the prerequisite fails.
	if !ok {
		return errors.New("runtime does not support shared collectors")
	}
	endpoint, tlsMaterial, err := c.collectorDeploymentTLS()
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return errors.New("Collector transport identity refresh failed")
	}
	endpoint, image, err := c.managedCollectorTargetEndpoint(endpoint)
	// Resolve verified image assets before issuing runtime credentials.
	if err != nil {
		return err
	}
	bundle, err := collector.Render(collector.Options{Runtime: c.opts.Runtime.Name(), Endpoint: endpoint, Image: image})
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return errors.New("Collector configuration is invalid")
	}
	credentials, err := c.prepareManagedCredential()
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return errors.New("Collector credential checkpoint or verifier could not be prepared")
	}
	token := credentials.Active
	// Handle this lifecycle state before continuing bounded reconciliation.
	if credentials.Pending != nil {
		token = credentials.Pending
	}
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err = runtime.EnsureCollector(ctx, model.CollectorDeployment{Endpoint: endpoint, Image: image, Token: token.Token, Config: bundle.Config, Manifest: bundle.Deployment, CAFile: c.opts.CollectorCAFile, TLS: tlsMaterial}); err != nil {
		return collectorRuntimeFailure(err)
	}
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err = c.completeManagedCredential(credentials, token); err != nil {
		return errors.New("Healthy collector credential rotation could not be finalized")
	}
	return nil
}

// collectorRuntimeFailure preserves reviewed runtime diagnostics without exposing arbitrary transport text.
func collectorRuntimeFailure(err error) error {
	var publicError *model.Error
	if errors.As(err, &publicError) {
		return fmt.Errorf("Runtime collector installation or readiness check failed: %s", publicError.Message)
	}
	return errors.New("Runtime collector installation or readiness check failed")
}

// managedCollectorImage opens verified release assets once and shares them with pull handlers.
func (c *Controller) managedCollectorImage(host string) (string, error) {
	c.managed.mu.Lock()
	defer c.managed.mu.Unlock()
	// Handle this lifecycle state before continuing bounded reconciliation.
	if c.managed.image == nil {
		image, err := collectorimage.Open(filepath.Join(c.opts.StateDir, "collector-image"))
		// Stop before publishing authority or readiness when the prerequisite fails.
		if err != nil {
			return "", err
		}
		c.managed.image = image
	}
	return c.managed.image.Reference(host), nil
}

// recordManagedCollector publishes bounded, nonsecret status and only logs transitions.
func (c *Controller) recordManagedCollector(err error) {
	c.managed.mu.Lock()
	prior := c.managed.health.State
	c.managed.health.LastAttemptAt = c.opts.Now().UTC()
	c.managed.health.State = "ready"
	c.managed.health.Reason = ""
	// Handle this lifecycle state before continuing bounded reconciliation.
	if err != nil {
		c.managed.health.State = "degraded"
		// Certificate acquisition is expected during first boot and must not imply a failed pull.
		if errors.Is(err, errCollectorHTTPSPending) && c.managed.health.LastReadyAt.IsZero() {
			c.managed.health.State = "starting"
		}
		c.managed.health.Reason = err.Error()
		// Handle this lifecycle state before continuing bounded reconciliation.
		if len(c.managed.health.Reason) > 200 {
			c.managed.health.Reason = "Shared collector provisioning failed"
		}
	} else {
		c.managed.health.LastReadyAt = c.opts.Now().UTC()
	}
	state := c.managed.health.State
	c.managed.mu.Unlock()
	// Handle this lifecycle state before continuing bounded reconciliation.
	if prior != state {
		c.mu.Lock()
		c.log("Managed collector "+state, "", "info")
		c.mu.Unlock()
	}
}

// managedCollectorHealth returns a detached observation without credentials or image paths.
func (c *Controller) managedCollectorHealth() *model.ManagedCollectorHealth {
	// Stop before publishing authority or readiness when the prerequisite fails.
	if !c.opts.ManagedCollector {
		return nil
	}
	c.managed.mu.RLock()
	defer c.managed.mu.RUnlock()
	result := c.managed.health
	// Handle this lifecycle state before continuing bounded reconciliation.
	if result.State == "" {
		result.State = "starting"
	}
	return &result
}

// closeManagedCollector waits for pull readers before releasing immutable image descriptors.
func (c *Controller) closeManagedCollector() {
	c.managed.mu.Lock()
	defer c.managed.mu.Unlock()
	// Handle this lifecycle state before continuing bounded reconciliation.
	if c.managed.image != nil {
		c.managed.image.Close()
		c.managed.image = nil
	}
}

// managedCollectorTarget validates an explicit HTTPS origin and resolves its immutable image reference.
func (c *Controller) managedCollectorTarget() (string, string, error) {
	return c.managedCollectorTargetEndpoint("")
}

// managedCollectorTargetEndpoint allows the isolated fleet collector origin to override the ordinary public endpoint.
func (c *Controller) managedCollectorTargetEndpoint(endpoint string) (string, string, error) {
	origin, err := c.managedCollectorOrigin(endpoint)
	// Validate the transport origin before opening or publishing image assets.
	if err != nil {
		return "", "", err
	}
	image, err := c.managedCollectorImage(origin.Host)
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return "", "", errors.New("Collector image assets are missing or invalid")
	}
	return origin.String(), image, nil
}

// managedCollectorOrigin resolves the exact registry/exporter origin without opening image assets.
func (c *Controller) managedCollectorOrigin(endpoint string) (*url.URL, error) {
	// Apply this transport-specific case without changing fleet management authority.
	if endpoint == "" {
		endpoint = c.opts.CollectorEndpoint
	}
	// Handle this lifecycle state before continuing bounded reconciliation.
	if endpoint == "" {
		endpoint = "https://" + c.opts.Hostname
	}
	origin, err := url.Parse(endpoint)
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil || !validCollectorOrigin(origin) {
		return nil, errors.New("Collector requires an HTTPS controller origin")
	}
	return origin, nil
}
