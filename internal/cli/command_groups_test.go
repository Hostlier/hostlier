package cli

import (
	"reflect"
	"strings"
	"testing"
)

// TestGroupedCommandsPreserveHandlerArguments checks all aliases, including position-sensitive handlers.
func TestGroupedCommandsPreserveHandlerArguments(t *testing.T) {
	// Verify every public group, including commands with multiple positional arguments.
	for group, entries := range commandGroups {
		// Each child must map to the unchanged handler argument layout.
		for _, entry := range entries {
			input := []string{group, entry.name, "target", "step", "--server", "production"}
			parsed, err := parse(input)
			// Invalid fixture arguments must fail before testing routing.
			if err != nil {
				t.Fatal(err)
			}
			routed, err := routeArguments(parsed)
			want := []string{entry.name, "target", "step"}
			// Existing server profile commands retain the server prefix.
			if group == "server" && (entry.name == "ssh" || entry.name == "join" || entry.name == "add" || entry.name == "list" || entry.name == "use") {
				want = append([]string{group}, want...)
			}
			// Routing must preserve both positions and the selected controller.
			if err != nil || !reflect.DeepEqual(routed.args, want) || routed.flags["server"] != "production" {
				t.Fatalf("%v: %+v %v", input, routed, err)
			}
		}
	}
}

// TestUnknownGroupedCommandsFailBeforeConnection prevents groups from forwarding arbitrary commands.
func TestUnknownGroupedCommandsFailBeforeConnection(t *testing.T) {
	// Unknown child commands must never fall through to remote execution.
	for _, group := range []string{"app", "server", "access"} {
		f := &fakeClient{}
		_, err := invoke([]string{group, "missing"}, "", Options{Client: f})
		// The diagnostic must identify the relevant help page without network calls.
		if err == nil || !strings.Contains(err.Error(), "hostlier "+group+" --help") || len(f.calls) != 0 {
			t.Fatalf("%s: %v %+v", group, err, f.calls)
		}
	}
}

// TestGroupedReadsUseExistingEndpoints exercises real dispatch after normalization.
func TestGroupedReadsUseExistingEndpoints(t *testing.T) {
	// Cover ordinary and position-sensitive read handlers through real CLI dispatch.
	for _, test := range []struct {
		args []string
		path string
	}{
		{[]string{"app", "list"}, "/v1/apps"},
		{[]string{"app", "list", "--all"}, "/v1/apps?includeDeleted=true"},
		{[]string{"app", "status", "demo"}, "/v1/apps/demo"},
		{[]string{"app", "workflow", "list", "demo"}, "/v1/workflows/demo"},
		{[]string{"server", "doctor"}, "/v1/doctor"},
		{[]string{"access", "clients", "list"}, "/v1/clients"},
	} {
		f := &fakeClient{}
		_, err := invoke(test.args, "", Options{Client: f})
		// Each read must reach exactly one existing endpoint.
		if err != nil || len(f.calls) != 1 {
			t.Fatalf("%v: %v %+v", test.args, err, f.calls)
		}
		// Grouping must not change the method or resource identity.
		if f.calls[0].method != "GET" || f.calls[0].path != test.path {
			t.Fatalf("%v: %+v", test.args, f.calls)
		}
	}
}

func TestAllFlagOnlyListsApplications(t *testing.T) {
	f := &fakeClient{}
	if _, err := invoke([]string{"status", "--all"}, "", Options{Client: f}); err == nil || len(f.calls) != 0 {
		t.Fatalf("--all escaped the app inventory: %v %+v", err, f.calls)
	}
}

// TestGroupedDeploymentStillRejectsYes preserves approval enforcement on grouped mutations.
func TestGroupedDeploymentStillRejectsYes(t *testing.T) {
	// All grouped deployment variants retain the same approval boundary.
	for _, command := range []string{"deploy", "update", "rollback", "scale", "sidecar"} {
		f := &fakeClient{}
		_, err := invoke([]string{"app", command, "--yes"}, "", Options{Client: f})
		// Reject bypass flags before sending any controller request.
		if err == nil || !strings.Contains(err.Error(), "--yes is not allowed") || len(f.calls) != 0 {
			t.Fatalf("%s: %v %+v", command, err, f.calls)
		}
	}
}
