package controller

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestActivityHistoryScopeAuthAndRestart(t *testing.T) {
	dir := t.TempDir()
	c := newTestController(t, dir, &recordingRuntime{}, nil)
	now := time.Now().UTC()
	c.mu.Lock()
	c.state.Operations["op-history"] = &Operation{ID: "op-history", Kind: "deploy", Name: "sample", Actor: "owner"}
	c.log("History test operation", "op-history", "info")
	if err := c.store.Save(c.state); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	if code, _ := call(t, c, false, "GET", "/v1/activity", nil, ""); code != 401 {
		t.Fatal("unauthenticated history", code)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	c = newTestController(t, dir, &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	q := url.Values{"app": {"sample"}, "q": {"History test operation"}, "from": {now.Add(-time.Hour).Format(time.RFC3339Nano)}, "to": {now.Add(time.Hour).Format(time.RFC3339Nano)}}
	status, page := call(t, c, true, "GET", "/v1/activity?"+q.Encode(), nil, "")
	raw, _ := json.Marshal(page)
	if status != 200 || !strings.Contains(string(raw), "History test operation") || !strings.Contains(string(raw), "owner") {
		t.Fatal(status, string(raw))
	}
	q.Set("app", "other")
	status, page = call(t, c, true, "GET", "/v1/activity?"+q.Encode(), nil, "")
	raw, _ = json.Marshal(page)
	if status != 200 || strings.Contains(string(raw), "History test operation") {
		t.Fatal("cross-app history", status, string(raw))
	}
	if status, _ = call(t, c, true, "GET", "/v1/activity?from=invalid", nil, ""); status != 400 {
		t.Fatal(status)
	}
}

func TestActivityHistoryFlushesProgressAndAuditsManagement(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	c.mu.Lock()
	c.log("Background progress without browser persistence", "", "info")
	c.mu.Unlock()
	deadline := time.Now().Add(4 * time.Second)
	for {
		status, page := call(t, c, true, "GET", "/v1/activity?q=Background+progress", nil, "")
		raw, _ := json.Marshal(page)
		if status == 200 && strings.Contains(string(raw), "Background progress without browser persistence") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime progress was not persisted", status, string(raw))
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, response := call(t, c, true, "POST", "/v1/domains", map[string]any{"domain": "activity.test"}, "")
	if status != 200 {
		t.Fatal(status, response)
	}
	status, page := call(t, c, true, "GET", "/v1/activity?q=Request+completed", nil, "")
	raw, _ := json.Marshal(page)
	if status != 200 || !strings.Contains(string(raw), "POST /v1/domains") || !strings.Contains(string(raw), `"actor":"local"`) {
		t.Fatal(status, string(raw))
	}
	if strings.Contains(string(raw), "activity.test") {
		t.Fatal("request body was copied into activity")
	}
}
