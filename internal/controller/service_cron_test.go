package controller

import (
	"context"
	"maps"
	"testing"

	"hostlier/internal/model"
)

type serviceCronTestRuntime struct {
	cronTestRuntime
	jobs          map[string]string
	failReconcile bool
}

func (r *serviceCronTestRuntime) ReconcileServiceCrons(_ context.Context, spec model.Project, _ model.DeployContext) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failReconcile {
		return model.Err("FIXTURE", "cron reconcile unavailable")
	}
	next := make(map[string]string, len(spec.Cron))
	for job := range spec.Cron {
		target := spec.Name + "/" + job
		next[target] = "scheduled"
		if r.jobs[target] == "paused" {
			next[target] = "paused"
		}
	}
	r.jobs = next
	return nil
}

func (r *serviceCronTestRuntime) ServiceCronStatus(_ context.Context, target string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if status, ok := r.jobs[target]; ok {
		return status, nil
	}
	return "", model.Err("NOT_FOUND", "Cron job not found")
}

func (r *serviceCronTestRuntime) CronAction(ctx context.Context, target, action, operation, revision string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.jobs[target]; !ok {
		return "", model.Err("NOT_FOUND", "Cron job not found")
	}
	switch action {
	case "pause":
		r.jobs[target] = "paused"
	case "resume":
		r.jobs[target] = "scheduled"
	case "delete":
		delete(r.jobs, target)
	}
	return r.cronTestRuntime.CronAction(ctx, target, action, operation, revision)
}

func TestServiceCronDeploymentAndCleanup(t *testing.T) {
	runtime := &serviceCronTestRuntime{}
	c := newTestController(t, t.TempDir(), runtime, nil)
	defer closeController(t, c)

	spec := project()
	spec.Cron = map[string]model.CronSchedule{
		"daily":  {Schedule: "@daily", Command: []string{"/app/report"}},
		"weekly": {Schedule: "@weekly", Command: []string{"/app/report"}},
	}
	request := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		status, result := call(t, c, true, method, path, body, "")
		if status != want {
			t.Fatalf("%s %s returned %d, want %d: %v", method, path, status, want, result)
		}
		return result
	}
	app := "/v1/apps/" + spec.Name
	job := app + "/cron/jobs/daily"
	apply := func(plan map[string]any, want string) {
		t.Helper()
		accepted := request("POST", "/v1/plans/"+plan["id"].(string)+"/apply", map[string]any{
			"fingerprint": plan["fingerprint"], "approved": true, "requestId": plan["id"],
		}, 200)
		op := wait(t, c, accepted["operationId"].(string))
		if op["status"] != want {
			t.Fatal(op)
		}
	}
	plan := func() map[string]any {
		t.Helper()
		return request("POST", "/v1/plans", map[string]any{"spec": spec}, 200)
	}
	change := plan()
	if change["changes"].(map[string]any)["cron"] == nil || scopedProject(spec, nil) {
		t.Fatalf("missing cron review or scoped protection: %v", change)
	}
	request("GET", job, nil, 404)
	apply(change, "succeeded")
	c.mu.Lock()
	version := c.state.Version
	c.mu.Unlock()
	if version != 5 {
		t.Fatalf("service cron state version = %d", version)
	}

	status := request("GET", job, nil, 200)
	if status["schedule"] != "@daily" || status["name"] != spec.Name+"/daily" {
		t.Fatal(status)
	}
	accepted := request("POST", job+"/action", map[string]any{"action": "pause", "revision": status["revision"]}, 200)
	if op := wait(t, c, accepted["operationId"].(string)); op["status"] != "succeeded" || op["cronJob"] != "daily" {
		t.Fatal(op)
	}
	if status := request("GET", job, nil, 200); status["status"] != "paused" {
		t.Fatal(status)
	}
	if service := request("GET", app, nil, 200); service["status"] != "running" {
		t.Fatal(service)
	}
	if runs := request("GET", job+"/runs", nil, 200)["runs"].([]any); len(runs) != 1 {
		t.Fatal(runs)
	}
	if logs := request("GET", job+"/runs/manual-fixture/logs?tail=1", nil, 200); logs["text"] != "finished\n" {
		t.Fatal(logs)
	}
	apply(plan(), "succeeded")
	if status := request("GET", job, nil, 200); status["status"] != "paused" {
		t.Fatal(status)
	}
	spec.Cron = maps.Clone(spec.Cron)
	delete(spec.Cron, "daily")
	change = plan()
	removed := change["changes"].(map[string]any)["cron"].(map[string]any)["remove"].([]any)
	if len(removed) != 1 || removed[0] != "daily" {
		t.Fatal(removed)
	}
	apply(change, "succeeded")
	request("GET", job, nil, 404)

	// An incomplete removal remains cleanup work even when the recorded service has no cron property.
	spec.Cron = nil
	runtime.mu.Lock()
	runtime.failReconcile = true
	runtime.mu.Unlock()
	apply(plan(), "failed")
	if service := request("GET", app, nil, 200); service["status"] != "running" || service["cronReconcilePending"] != true {
		t.Fatal(service)
	}
	runtime.mu.Lock()
	runtime.failReconcile = false
	runtime.mu.Unlock()
	accepted = request("POST", app+"/action", map[string]any{"action": "delete"}, 200)
	if op := wait(t, c, accepted["operationId"].(string)); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.jobs) != 0 {
		t.Fatal(runtime.jobs)
	}
}
