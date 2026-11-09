package controller

import (
	"testing"
	"time"
)

func TestInstanceOwnedSecretsAreBoundAndPurgedByController(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	for _, item := range []struct{ name, owner string }{{"owner-key", "sample"}, {"global-key", ""}} {
		body := map[string]any{"value": "private-value", "requestId": item.name}
		if item.owner != "" {
			body["owner"] = item.owner
		}
		if status, _ := call(t, c, true, "PUT", "/v1/secrets/"+item.name, body, ""); status != 200 {
			t.Fatal("secret registration failed", item.name, status)
		}
	}
	other := project()
	other.Name = "other"
	other.Secrets = map[string]string{"KEY": "owner-key"}
	if _, err := c.references(other, nil); err == nil {
		t.Fatal("another app used an owned secret")
	}
	app := &Application{SecretRefs: map[string]SecretRef{"KEY": {Name: "owner-key"}}}
	app.Name = "sample"
	c.mu.Lock()
	c.state.Applications[app.Name] = app
	c.mu.Unlock()
	if status, _ := call(t, c, true, "POST", "/v1/apps/sample/action", map[string]any{"action": "delete", "purge": true}, ""); status != 200 {
		t.Fatal("purge action was rejected", status)
	}
	for _, op := range c.state.Operations {
		if op.Name == "sample" && op.Action == "delete" && op.Purge {
			wait(t, c, op.ID)
		}
	}
	if c.state.Applications["sample"] != nil || c.state.Secrets["owner-key"] != nil || c.state.Secrets["global-key"] == nil {
		t.Fatal("purge did not retire exactly the instance-owned secrets")
	}
	for _, write := range c.state.Writes {
		if write.Name == "owner-key" {
			t.Fatal("purge retained an owned secret upload receipt")
		}
	}
}
