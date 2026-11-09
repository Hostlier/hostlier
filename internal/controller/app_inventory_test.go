package controller

import (
	"context"
	"testing"

	"hostlier/internal/model"
)

func TestDeletedAppsStayRetainedButLeaveDefaultInventory(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	c.state.Applications["live"] = &Application{Deployment: model.Deployment{Name: "live", Status: "running"}}
	c.state.Applications["removed"] = &Application{Deployment: model.Deployment{Name: "removed", Status: "deleted"}}
	if err := c.store.Save(c.state); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/apps", "/v1/status"} {
		status, out := call(t, c, true, "GET", path, nil, "")
		apps, ok := out["applications"].([]any)
		if status != 200 || !ok || len(apps) != 1 || apps[0].(map[string]any)["name"] != "live" {
			t.Fatalf("%s exposed deleted app: %d %v", path, status, out)
		}
	}
	status, out := call(t, c, true, "GET", "/v1/apps?includeDeleted=true", nil, "")
	if apps, ok := out["applications"].([]any); status != 200 || !ok || len(apps) != 2 {
		t.Fatalf("retained inventory unavailable: %d %v", status, out)
	}
	status, out = call(t, c, true, "GET", "/v1/apps/removed", nil, "")
	if status != 200 || out["status"] != "deleted" {
		t.Fatalf("retained app status unavailable: %d %v", status, out)
	}
}
