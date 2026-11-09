package controller

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"net/http"
	"time"
)

// serveActivity reads committed history without saving state or accepting mutation authority.
func (c *Controller) serveActivity(w http.ResponseWriter, r *http.Request, local bool) bool {
	if r.URL.Path != "/v1/activity" {
		return false
	}
	if err := c.statusAuthority(r, local); err != nil {
		eventStreamError(w, err)
		return true
	}
	if r.Method != "GET" {
		eventStreamError(w, model.Err("NOT_FOUND", "Endpoint not found"))
		return true
	}
	filter, err := model.ParseHistoryFilter(r.URL.Query())
	if err != nil {
		eventStreamError(w, err)
		return true
	}
	app, operation := r.URL.Query().Get("app"), r.URL.Query().Get("operationId")
	if len(app) > 200 || len(operation) > 200 {
		eventStreamError(w, model.Err("INVALID_REQUEST", "Invalid history identity"))
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	page, err := c.store.Activity(ctx, filter, app, operation)
	if err != nil {
		eventStreamError(w, model.Err("UNAVAILABLE", "Activity history is unavailable"))
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(page)
	return true
}

// Runtime progress can arrive between state transitions. Flush new events without
// making a browser read own persistence or the lifetime of the operation.
func (c *Controller) startActivityPersistence() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				c.mu.Lock()
				if !c.closed && c.state.EventSequence > c.eventPersistedSequence {
					if err := c.store.Save(c.state); err == nil {
						c.eventPersistedSequence = c.state.EventSequence
					}
				}
				c.mu.Unlock()
			}
		}
	}()
}
