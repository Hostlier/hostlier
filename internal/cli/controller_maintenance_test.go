package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
)

// maintenanceRequester simulates only existing authenticated command RPC routes and retained approvals.
type maintenanceRequester struct {
	approval      approvalSimulation
	calls         []string
	old, blockers bool
	polls         int
}

// Request rejects SSH assumptions and emits a temporary outage during the accepted update.
func (r *maintenanceRequester) Request(_ context.Context, method, path string, body, result any) error {
	handled, call, err := r.approval.respond(method, path, body, result)
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if handled {
		return err
	}
	method, path = call.method, call.path
	r.calls = append(r.calls, method+" "+path)
	var value any
	// Alter only the trust binding selected by this fixture.
	switch {
	// Exercise this independent maintenance outcome with the same retained request.
	case path == "/v1/controller/capabilities":
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if r.old {
			return model.Err("NOT_FOUND", "Endpoint not found")
		}
		value = map[string]bool{"updates": true, "migrationPreview": true}
	// Exercise this independent maintenance outcome with the same retained request.
	case path == "/v1/controller/update/plan":
		plan := model.ControllerUpdatePlan{ID: strings.Repeat("a", 32), Fingerprint: "exact", Version: "v1.2.3", Runtime: "docker"}
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if r.blockers {
			plan.Errors = []string{"fixture preflight blocker"}
		}
		value = plan
	// Exercise this independent maintenance outcome with the same retained request.
	case strings.HasPrefix(path, "/v1/controller/migration/plan?"):
		value = map[string]any{"to": "k3s", "errors": []string{"fixture migration blocker"}}
	// Exercise this independent maintenance outcome with the same retained request.
	case path == "/v1/controller/update/apply":
		value = model.ControllerUpdateJob{ID: strings.Repeat("a", 32), Status: "queued"}
	// Exercise this independent maintenance outcome with the same retained request.
	case strings.HasPrefix(path, "/v1/controller/update/jobs/"):
		r.polls++
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if r.polls < 3 {
			return model.Err("CONNECTION", "API restarting")
		}
		value = model.ControllerUpdateJob{ID: strings.Repeat("a", 32), Status: "succeeded"}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	default:
		return model.Err("UNEXPECTED_REQUEST", method+" "+path)
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, result)
}

// runMaintenanceCLI exercises real grouped dispatch, profile selection and retained mutation machinery.
func runMaintenanceCLI(t *testing.T, r *maintenanceRequester, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	// Keep this fixture callback bound to the controlled maintenance outcome.
	options := Options{Home: t.TempDir(), Client: r, PollInterval: time.Millisecond, RunSSH: func(context.Context, []string) ([]byte, error) { t.Fatal("maintenance attempted SSH"); return nil, nil }}
	err := RunWithOptions(context.Background(), append([]string{"server", "controller"}, args...), strings.NewReader(""), &out, io.Discard, options)
	return out.String(), err
}

// TestControllerMaintenanceCLIReadOnly verifies previews never submit a privileged update request.
func TestControllerMaintenanceCLIReadOnly(t *testing.T) {
	// Check each maintenance case without sharing its accepted execution state.
	for _, args := range [][]string{{"update", "--dry-run"}, {"migrate", "plan", "--to", "k3s", "--backup-dir", "/var/backups/hostlier/new"}} {
		r := &maintenanceRequester{}
		output, err := runMaintenanceCLI(t, r, args...)
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if err != nil || output == "" {
			t.Fatal(output, err)
		}
		// Check each maintenance case without sharing its accepted execution state.
		for _, call := range r.calls {
			// Assert the expected maintenance receipt, authorization or recovery behavior.
			if strings.HasPrefix(call, "POST") {
				t.Fatal("preview mutated server", r.calls)
			}
		}
	}
}

// TestControllerMaintenanceCLIReconnect verifies the original accepted job is observed across API downtime.
func TestControllerMaintenanceCLIReconnect(t *testing.T) {
	r := &maintenanceRequester{}
	output, err := runMaintenanceCLI(t, r, "update")
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if err != nil || !strings.Contains(output, "succeeded") || r.polls != 3 {
		t.Fatal(output, err, r.calls)
	}
	applications := 0
	// Check each maintenance case without sharing its accepted execution state.
	for _, call := range r.calls {
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if call == "POST /v1/controller/update/apply" {
			applications++
		}
	}
	// Assert the expected maintenance receipt, authorization or recovery behavior.
	if applications != 1 {
		t.Fatal("update replayed", r.calls)
	}
}

// TestControllerMaintenanceCLIBlocksUnsafeApply verifies old controllers and preflight blockers produce no update.
func TestControllerMaintenanceCLIBlocksUnsafeApply(t *testing.T) {
	// Check each maintenance case without sharing its accepted execution state.
	for _, kind := range []string{"old", "blocked", "yes"} {
		r := &maintenanceRequester{old: kind == "old", blockers: kind == "blocked"}
		args := []string{"update"}
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if kind == "yes" {
			args = append(args, "--yes")
		}
		_, err := runMaintenanceCLI(t, r, args...)
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if err == nil {
			t.Fatal("accepted", kind)
		}
		// Assert the expected maintenance receipt, authorization or recovery behavior.
		if kind == "old" && planningErrorCode(err) != "CONTROLLER_UPGRADE_REQUIRED" {
			t.Fatal(err)
		}
		// Check each maintenance case without sharing its accepted execution state.
		for _, call := range r.calls {
			// Assert the expected maintenance receipt, authorization or recovery behavior.
			if strings.HasPrefix(call, "POST") {
				t.Fatal("rejected update applied", r.calls)
			}
		}
	}
}
