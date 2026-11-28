package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"hostlier/internal/model"
)

type cronTestRuntime struct{ recordingRuntime }

func (*cronTestRuntime) Name() string { return "k3s" }

func (r *cronTestRuntime) DeployCron(_ context.Context, p model.Project, dc model.DeployContext) (model.Deployment, error) {
	if !strings.Contains(p.Image, "@sha256:") || dc.OperationID == "" || p.TimeZone != "Etc/UTC" {
		return model.Deployment{}, fmt.Errorf("invalid approved schedule: %s, %s, %s", p.Image, dc.OperationID, p.TimeZone)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return model.Deployment{ImageDigest: p.Image, RuntimeID: "schedule", Status: "scheduled"}, nil
}

func (*cronTestRuntime) CronAction(_ context.Context, _, action, _, revision string) (string, error) {
	if revision == "" {
		return "", fmt.Errorf("missing schedule revision")
	}
	if action == "run" {
		return "manual-fixture", nil
	}
	return "", nil
}

func (*cronTestRuntime) CronRuns(context.Context, string) ([]model.CronRun, error) {
	return []model.CronRun{{ID: "manual-fixture", Status: "succeeded", Trigger: "manual"}}, nil
}

func (*cronTestRuntime) CronLogs(_ context.Context, _, run string, tail int) (string, error) {
	if run != "manual-fixture" || tail != 1 {
		return "", fmt.Errorf("wrong run or log tail: %s, %d", run, tail)
	}
	return "finished\n", nil
}

func TestCronPlanAndLifecycle(t *testing.T) {
	runtime := &cronTestRuntime{}
	c := newTestController(t, t.TempDir(), runtime, nil)
	defer closeController(t, c)
	spec := model.Project{Version: 2, Kind: model.CronKind, Name: "report", Source: &model.Source{Image: "example/report:1"}, Schedule: "0 7 * * 1-5"}
	request := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		status, result := call(t, c, true, method, path, body, "")
		if status != want {
			t.Fatalf("%s %s returned %d, want %d: %v", method, path, status, want, result)
		}
		return result
	}
	plan := request("POST", "/v1/plans", map[string]any{"spec": spec}, 200)
	changes := plan["changes"].(map[string]any)
	if changes["schedule"] != spec.Schedule || changes["kind"] != model.CronKind || changes["timeZone"] != "Etc/UTC" {
		t.Fatalf("schedule missing from plan: %v", changes)
	}
	runtime.mu.Lock()
	calls := runtime.calls
	runtime.mu.Unlock()
	if calls != 0 {
		t.Fatal("planning created a schedule")
	}
	path := "/v1/plans/" + plan["id"].(string) + "/apply"
	body := map[string]any{"fingerprint": plan["fingerprint"], "requestId": "register-cron"}
	request("POST", path, body, 403)
	body["approved"] = true
	accepted := request("POST", path, body, 200)
	op := wait(t, c, accepted["operationId"].(string))
	if op["status"] != "succeeded" {
		t.Fatal(op)
	}
	c.mu.Lock()
	version := c.state.Version
	c.mu.Unlock()
	if version != 4 {
		t.Fatalf("scheduled work was persisted with state version %d", version)
	}
	app := "/v1/apps/" + spec.Name
	cron := app + "/cron"
	schedule := request("GET", cron, nil, 200)
	if schedule["status"] != "scheduled" {
		t.Fatal(schedule)
	}
	defaultSwarm := request("GET", "/v1/swarms/default/apps", nil, 200)
	apps := defaultSwarm["applications"].([]any)
	if len(apps) != 1 || apps[0].(map[string]any)["name"] != spec.Name {
		t.Fatal(defaultSwarm)
	}
	service := project()
	service.Name = spec.Name
	request("POST", "/v1/plans", map[string]any{"spec": service}, 400)
	request("POST", app+"/action", map[string]any{"action": "delete"}, 400)
	request("POST", cron+"/action", map[string]any{"action": "run", "revision": "old-revision"}, 409)
	if scopedProject(spec, nil) || grantAllowsPlan(&Authorization{}, Plan{Spec: spec}, humanRequest{}) {
		t.Fatal("a bounded deployment grant can register a persistent schedule")
	}
	for _, action := range []string{"pause", "run", "resume", "delete"} {
		accepted := request("POST", cron+"/action", map[string]any{"action": action, "revision": schedule["revision"]}, 200)
		op := wait(t, c, accepted["operationId"].(string))
		if op["status"] != "succeeded" {
			t.Fatal(op)
		}
		switch action {
		case "pause":
			if result := request("GET", cron, nil, 200); result["status"] != "paused" {
				t.Fatal(result)
			}
		case "run":
			if op["runId"] != "manual-fixture" {
				t.Fatal(op)
			}
			history := request("GET", cron+"/runs", nil, 200)["runs"].([]any)
			if len(history) != 1 || history[0].(map[string]any)["trigger"] != "manual" {
				t.Fatal(history)
			}
			if logs := request("GET", cron+"/runs/manual-fixture/logs?tail=1", nil, 200); logs["text"] != "finished\n" {
				t.Fatal(logs)
			}
		}
	}
	request("GET", cron, nil, 404)
}

func TestCronRequiresK3s(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	spec := model.Project{Version: 2, Kind: model.CronKind, Name: "report", Source: &model.Source{Image: "example/report:1"}, Schedule: "@daily"}
	_, result := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	if code(result) != "CONTROLLER_MODE" {
		t.Fatal(result)
	}
}
