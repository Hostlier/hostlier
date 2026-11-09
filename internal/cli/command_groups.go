package cli

import (
	"fmt"
	"strings"

	"hostlier/internal/model"
)

// helpEntry describes one command at its current level of the command tree.
type helpEntry struct {
	name, summary string
}

// rootCommands expose domains; individual application and administration actions live below them.
var rootCommands = []helpEntry{
	{"init", "Create an app deployment configuration"},
	{"app", "Deploy and manage applications"},
	{"minion", "Build and test deterministic apps"},
	{"server", "Connect to and manage servers"},
	{"access", "Manage access and approvals"},
	{"resource", "Manage databases"},
	{"secret", "Manage application secrets"},
	{"backup", "Back up and recover data"},
	{"ui", "Open the dashboard"},
	{"operation", "Inspect operation progress"},
	{"telemetry", "Inspect metrics and log history"},
	{"fused", "Connect to Fused Engine"},
	{"tunnel", "Manage tunnels"},
	{"package", "Install provider packages"},
	{"sdk", "Install the package SDK"},
	{"skills", "Read or install offline guides"},
}

// commandGroups route new grouped spellings to the existing command handlers.
var commandGroups = map[string][]helpEntry{
	"app": {
		{"init", "Detect an app and create .hostlier"},
		{"deploy", "Deploy an application"},
		{"update", "Update an application"},
		{"rollback", "Restore a previous revision"},
		{"list", "List applications"},
		{"status", "Show application status"},
		{"logs", "Read or follow logs"},
		{"start", "Start an application"},
		{"stop", "Stop an application"},
		{"restart", "Restart an application"},
		{"delete", "Remove an application"},
		{"validate", "Validate a project file"},
		{"plan", "Preview deployment changes"},
		{"scale", "Change application capacity"},
		{"usage", "Show resource usage"},
		{"sidecar", "Manage sidecars"},
		{"product", "Plan or deploy a product"},
		{"workflow", "Inspect or recover workflow steps"},
	},
	"server": {
		{"join", "Connect this installed box to a master"},
		{"add", "Install a controller and enroll it with the selected master"},
		{"list", "List saved server profiles"},
		{"use", "Select a server profile"},
		{"connect", "Connect or renew a server profile"},
		{"reauth", "Alias for connect PROFILE"},
		{"disconnect", "Remove a server profile"},
		{"domains", "Manage allowed domains"},
		{"hosts", "Manage enrolled hosts"},
		{"controller", "Manage the controller"},
		{"agent", "Run a host agent"},
		{"doctor", "Check server health"},
	},
	"access": {
		{"approve", "Approve a reviewed request"},
		{"approval", "Track an approval request"},
		{"authorize", "Authorize a deployment window"},
		{"authorizations", "List or revoke deployment windows"},
		{"security", "Manage approval keys"},
		{"pairing", "Manage device enrollment"},
		{"clients", "List or revoke clients"},
	},
}

// routeArguments normalizes grouped commands before approval, positional checks and dispatch.
func routeArguments(parsed arguments) (arguments, error) {
	offset := 0
	// Explicit help uses the same command tree as the --help flag.
	if len(parsed.args) > 0 && parsed.args[0] == "help" {
		offset = 1
	}
	// Empty invocations retain the root help path.
	if len(parsed.args) <= offset {
		return parsed, nil
	}
	group := parsed.args[offset]
	entries, ok := commandGroups[group]
	// Existing top-level spellings continue through their original handlers.
	if !ok {
		return parsed, nil
	}
	// A bare group shows its children without opening a profile or controller.
	if len(parsed.args) == offset+1 {
		parsed.flags["help"] = "true"
		return parsed, nil
	}
	action := parsed.args[offset+1]
	// Only advertised actions may be routed through a group.
	if !groupHasCommand(entries, action) {
		return parsed, model.Err("ARGUMENTS", fmt.Sprintf("Unknown command %q; run hostlier %s --help", group+" "+action, group))
	}
	parsed.commandPath = group + " " + action
	// Server profile actions already use this positional shape.
	if group == "server" && (action == "join" || action == "add" || action == "list" || action == "use") {
		return parsed, nil
	}
	args := append([]string(nil), parsed.args[:offset]...)
	parsed.args = append(args, parsed.args[offset+1:]...)
	return parsed, nil
}

// groupHasCommand checks literal names instead of accepting arbitrary forwarded commands.
func groupHasCommand(entries []helpEntry, name string) bool {
	// Keep group membership explicit for both help and execution.
	for _, entry := range entries {
		// Return only an exact advertised match.
		if entry.name == name {
			return true
		}
	}
	return false
}

// commandIndex renders one level of the CLI without showing child usage or reference prose.
func commandIndex(path, summary string, entries []helpEntry) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n\nUsage:\n  %s [command]\n\nAvailable Commands:\n", summary, path)
	// Descriptions are deliberately short enough to scan in a terminal.
	for _, entry := range entries {
		fmt.Fprintf(&out, "  %-15s %s\n", entry.name, entry.summary)
	}
	fmt.Fprintf(&out, "\nFlags:\n  -h, --help  Show help\n  --json      JSON output (the default when output is piped)\n  --toon      TOON output, compact for LLM agents\n\nUse \"%s [command] --help\" for more information.", path)
	return out.String()
}

// groupSummary returns the same description at root and group level.
func groupSummary(name string) string {
	// Reuse the root description when opening a group.
	for _, entry := range rootCommands {
		// Return only an exact advertised match.
		if entry.name == name {
			return entry.summary
		}
	}
	return name
}
