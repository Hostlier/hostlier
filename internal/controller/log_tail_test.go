package controller

import (
	"context"
	"hostlier/internal/model"
	"testing"
	"time"
)

// tailRuntime records the line policy delivered to the runtime interface.
type tailRuntime struct {
	recordingRuntime
	tail, calls int
}

// Logs returns enough history to verify the controller also bounds third-party backend responses.
func (r *tailRuntime) Logs(ctx context.Context, name string) (string, error) {
	r.tail, r.calls = model.LogTail(ctx), r.calls+1
	return "one\ntwo\nthree\n", nil
}

// TestApplicationLogTail verifies endpoint parsing, default compatibility and runtime propagation.
func TestApplicationLogTail(t *testing.T) {
	runtime := &tailRuntime{}
	c := newTestController(t, t.TempDir(), runtime, time.Now)
	defer closeController(t, c)
	c.state.Applications["api"] = &Application{Deployment: model.Deployment{Name: "api"}}
	// Omitted and explicit counts reach the runtime with their intended defaults.
	for _, test := range []struct {
		query string
		count int
		want  string
	}{{"", 200, "one\ntwo\nthree\n"}, {"?tail=2", 2, "two\nthree\n"}, {"?tail=0", 0, ""}, {"?tail=500", 500, "one\ntwo\nthree\n"}} {
		status, result := humanHTTP(t, c, true, "GET", "/v1/apps/api/logs"+test.query, nil, "", "")
		// Runtime policy and returned lines must agree with the explicit query.
		if status != 200 || result["text"] != test.want || runtime.tail != test.count {
			t.Fatal(test, status, result, runtime.tail)
		}
	}
	calls := runtime.calls
	// Invalid and ambiguous public limits fail before any runtime access.
	for _, query := range []string{"?tail=all", "?tail=-1", "?tail=10001", "?tail=", "?tail=1&tail=2"} {
		status, _ := humanHTTP(t, c, true, "GET", "/v1/apps/api/logs"+query, nil, "", "")
		// Invalid public input must not trigger a backend read.
		if status == 200 || runtime.calls != calls {
			t.Fatal("invalid query reached runtime", query)
		}
	}
}
