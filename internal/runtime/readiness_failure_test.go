package runtime

import (
	"encoding/json"
	"testing"
)

// TestPodReadinessFailure reports only explicit Kubernetes scheduling or memory evidence.
func TestPodReadinessFailure(t *testing.T) {
	cases := []struct{ name, raw, code string }{
		{"pending", `{"status":{"conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable"}]}}`, "UNSCHEDULABLE"},
		{"other-condition", `{"status":{"conditions":[{"type":"Ready","status":"False","reason":"Unschedulable"}]}}`, ""},
		{"current-oom", `{"status":{"containerStatuses":[{"state":{"terminated":{"reason":"OOMKilled"}}}]}}`, "APPLICATION_OOM"},
		{"last-oom", `{"status":{"containerStatuses":[{"lastState":{"terminated":{"reason":"OOMKilled"}}}]}}`, "APPLICATION_OOM"},
		{"ordinary-exit", `{"status":{"containerStatuses":[{"state":{"terminated":{"reason":"Error"}}}]}}`, ""},
		{"missing", `{}`, ""},
	}
	// Each case checks public error codes without depending on secret-containing diagnostic text.
	for _, test := range cases {
		// Independent subtests identify the observed status shape being classified.
		t.Run(test.name, func(t *testing.T) {
			var pod map[string]any
			// Test fixtures must decode exactly like actual Kubernetes JSON responses.
			if err := json.Unmarshal([]byte(test.raw), &pod); err != nil {
				t.Fatal(err)
			}
			err := podReadinessFailure(pod)
			// Unknown or healthy evidence must preserve the original rollout error.
			if test.code == "" {
				// An invented diagnosis would mislead the user about an unobserved cause.
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			// Known runtime evidence must yield its stable actionable error code.
			if !hasCode(err, test.code) {
				t.Fatalf("want %s, got %v", test.code, err)
			}
		})
	}
}
