package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// PlanningJob retains actor-bound preparation independently of an HTTP connection.
type PlanningJob struct {
	ID         string       `json:"id"`
	Status     string       `json:"status"`
	PlanID     string       `json:"planId,omitempty"`
	Error      *model.Error `json:"error,omitempty"`
	Actor      string       `json:"actor,omitempty"`
	RequestID  string       `json:"requestId,omitempty"`
	IntentHash string       `json:"intentHash,omitempty"`
	Intent     string       `json:"intent,omitempty"`
	Generation int64        `json:"generation"`
}

// planningRequest provides an idempotency identity for an encrypted deployment intent.
type planningRequest struct {
	RequestID string                `json:"requestId"`
	Request   deploymentPlanRequest `json:"request"`
}

// planningView excludes private request intent and ownership from public job receipts.
type planningView struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Plan   *Plan        `json:"plan,omitempty"`
	Error  *model.Error `json:"error,omitempty"`
}

// apiPlanning accepts durable preparation or observes one actor's existing preparation.
func (c *Controller) apiPlanning(r *http.Request, p []string, actor string) (any, error) {
	// New preparations need an explicit caller request identity.
	if r.Method == "POST" && len(p) == 2 {
		return c.acceptPlanning(r, actor)
	}
	// Polling never grants access to another actor's private reviewed specification.
	if r.Method == "GET" && len(p) == 3 {
		job := c.state.PlanningJobs[p[2]]
		// Hide private preparations from every other authenticated actor.
		if job == nil || job.Actor != actor {
			return nil, model.Err("NOT_FOUND", "Planning job not found")
		}
		return c.planningView(job), nil
	}
	return endpointNotFound()
}

// acceptPlanning persists an immutable encrypted request before starting background inspection.
func (c *Controller) acceptPlanning(r *http.Request, actor string) (any, error) {
	var request planningRequest
	// Unknown fields cannot become hidden authority in a later deployment plan.
	if err := decode(r, &request); err != nil {
		return nil, err
	}
	// Stop before publishing an incomplete or invalid preparation.
	if err := required(request.RequestID); err != nil {
		return nil, err
	}
	hash := config.Fingerprint(request.Request)
	// Repeated requests reconnect to the same durable preparation.
	for _, job := range c.state.PlanningJobs {
		// Hide private preparations from every other authenticated actor.
		if job.Actor == actor && job.RequestID == request.RequestID {
			// Reject reuse of one request identity for changed desired inputs.
			if job.IntentHash != hash {
				return nil, model.Err("IDEMPOTENCY_CONFLICT", "Planning request identity already used")
			}
			return c.planningView(job), nil
		}
	}
	// Bound retained requests independently of caller-provided identifiers.
	if len(c.state.PlanningJobs) >= 1000 {
		return nil, model.Err("RATE_LIMITED", "Planning job capacity reached")
	}
	return c.startPlanning(request, actor, hash)
}

// startPlanning encrypts accepted intent and schedules work only after durable persistence.
func (c *Controller) startPlanning(request planningRequest, actor, hash string) (any, error) {
	// Shutdown cannot accept work that has no live background lifetime.
	if c.closed {
		return nil, model.Err("REQUEST_FAILED", "Controller is shutting down")
	}
	raw, err := json.Marshal(request.Request)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return nil, err
	}
	sealed, err := c.store.Encrypt(string(raw))
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return nil, err
	}
	job := &PlanningJob{ID: random(), Status: "queued", Actor: actor, RequestID: request.RequestID, IntentHash: hash, Intent: sealed, Generation: c.state.Generation}
	c.state.PlanningJobs[job.ID] = job
	// The accepted job survives a lost POST response or an immediate controller restart.
	if err := c.store.Save(c.state); err != nil {
		delete(c.state.PlanningJobs, job.ID)
		return nil, err
	}
	c.wg.Add(1)
	go c.runPlanning(job.ID)
	return c.planningView(job), nil
}

// planningView returns completed plans through the existing exact approval boundary.
func (c *Controller) planningView(job *PlanningJob) planningView {
	view := planningView{ID: job.ID, Status: job.Status, Error: job.Error}
	// A completed receipt exposes the same immutable plan later used by applyPlan.
	if plan := c.state.Plans[job.PlanID]; plan != nil && job.Status == "completed" {
		copy := *plan
		copy.Actor = ""
		view.Plan = &copy
	}
	return view
}

// runPlanning owns a bounded controller lifetime rather than the initiating HTTP request.
func (c *Controller) runPlanning(id string) {
	defer c.wg.Done()
	c.mu.Lock()
	defer c.mu.Unlock()
	job := c.state.PlanningJobs[id]
	// Earlier restart recovery or shutdown cannot be undone by a queued worker.
	if job == nil || job.Status != "queued" || c.closed {
		return
	}
	defer c.signalJobChange()
	c.signalJobChange()
	job.Status = "running"
	// An unavailable durable store prevents provider inspection.
	if err := c.store.Save(c.state); err != nil {
		c.failPlanning(job, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.opts.PlanningTimeout)
	defer cancel()
	plan, err := c.executePlanning(ctx, job)
	// A generation change at any stage invalidates the entire reviewed snapshot.
	if err == nil && (job.Generation != c.state.Generation || c.closed || ctx.Err() != nil) {
		delete(c.state.Plans, plan.ID)
		err = model.Err("STALE_PLAN", "Server changed during planning; review a new plan")
	}
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		c.failPlanning(job, err)
		return
	}
	job.Status, job.PlanID = "completed", plan.ID
	c.store.Save(c.state)
}

// executePlanning decrypts the original intent only while preparing its immutable plan.
func (c *Controller) executePlanning(ctx context.Context, job *PlanningJob) (*Plan, error) {
	// Reject queued intent after any intervening authoritative mutation.
	if job.Generation != c.state.Generation {
		return nil, model.Err("STALE_PLAN", "Server changed before planning; review a new plan")
	}
	raw, err := c.store.Decrypt(job.Intent)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", "/v1/plans", bytes.NewBufferString(raw))
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return nil, err
	}
	// Keep private preparation within its bounded validated contract.
	defer func() { io.Copy(io.Discard, request.Body); request.Body.Close() }()
	result, err := c.makePlan(request, job.Actor)
	// Stop before publishing an incomplete or invalid preparation.
	if err != nil {
		return nil, err
	}
	plan, ok := result.(*Plan)
	// Keep private preparation within its bounded validated contract.
	if !ok {
		return nil, model.Err("REQUEST_FAILED", "Planning failed")
	}
	return plan, nil
}

// failPlanning persists safe public failure details without exposing package diagnostics.
func (c *Controller) failPlanning(job *PlanningJob, err error) {
	job.Status = "failed"
	job.Error = &model.Error{Code: "PACKAGE_PLAN_FAILED", Message: "Planning failed; review configuration and retry with a new request identity"}
	var safe *model.Error
	// Only recognized controller errors cross the private planning boundary.
	if errors.As(err, &safe) {
		job.Error = safe
	}
	// Cancellation during shutdown requires a fresh plan after restart.
	if c.closed {
		job.Status = "interrupted"
	}
	c.store.Save(c.state)
}

// initializePlanning migrates additive state and forbids replay of interrupted preparations.
func (c *Controller) initializePlanning() {
	// Keep private preparation within its bounded validated contract.
	if c.state.PlanningJobs == nil {
		c.state.PlanningJobs = map[string]*PlanningJob{}
	}
	// Restart recovery records interruption; it never automatically invokes apply.
	for _, job := range c.state.PlanningJobs {
		// Transition only unfinished preparations into explicit recovery state.
		if job.Status == "queued" || job.Status == "running" {
			job.Status = "interrupted"
			job.Error = &model.Error{Code: "RECOVERY_REQUIRED", Message: "Planning was interrupted; request a new plan"}
		}
	}
}
