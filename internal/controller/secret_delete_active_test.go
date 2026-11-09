package controller

import (
	"testing"
	"time"
)

func TestSecretDeletionRefusesActiveAppAndIsIdempotentAfterPurge(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	call(t, c, true, "PUT", "/v1/secrets/app-key", map[string]any{"value": "private-value", "requestId": "create-app-key"}, "")
	app := &Application{SecretRefs: map[string]SecretRef{"KEY": {Name: "app-key"}}}
	app.Name = "sample"
	c.mu.Lock()
	c.state.Applications[app.Name] = app
	c.mu.Unlock()
	if status, _ := call(t, c, true, "DELETE", "/v1/secrets/app-key", nil, ""); status != 409 {
		t.Fatal("active app secret was deleted", status)
	}
	c.mu.Lock()
	delete(c.state.Applications, app.Name)
	c.mu.Unlock()
	for range 2 {
		if status, _ := call(t, c, true, "DELETE", "/v1/secrets/app-key", nil, ""); status != 200 {
			t.Fatal("unused secret deletion was not idempotent", status)
		}
	}
}
