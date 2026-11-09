package cli

import (
	"strings"
	"testing"

	bundled "hostlier/internal/skills"
)

// offlineHelp asserts help never requires a profile, network or approval.
func offlineHelp(t *testing.T, args ...string) string {
	t.Helper()
	f := &fakeClient{}
	output, err := invoke(args, "", Options{Client: f, Home: t.TempDir()})
	// Help must be bounded and must not fall through to command execution.
	if err != nil || len(f.calls) != 0 || strings.Count(output, "\n") > 32 {
		t.Fatalf("%v: err=%v calls=%v output=%s", args, err, f.calls, output)
	}
	return output
}

// TestRootHelpShowsOnlyCommandDomains prevents leaf commands and reference prose leaking into root help.
func TestRootHelpShowsOnlyCommandDomains(t *testing.T) {
	// Root help supports no arguments and both conventional flag forms.
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"help"}} {
		output := offlineHelp(t, args...)
		// Root help should expose groups with descriptions and one next-step instruction.
		if !strings.Contains(output, "Available Commands:") || !strings.Contains(output, `hostlier [command] --help`) {
			t.Fatal(output)
		}
		// Deployment options, approval internals and the flat legacy command list are not root help.
		for _, excluded := range []string{"rollback", "--revision", "hardware", "Commands: fused", "--file", "--cpu"} {
			// Reject any leaked lower-level detail.
			if strings.Contains(output, excluded) {
				t.Fatalf("root help contains %q: %s", excluded, output)
			}
		}
	}
}

// TestGroupHelpShowsOneLevel covers both conventional help forms and bare groups.
func TestGroupHelpShowsOneLevel(t *testing.T) {
	// Each group must work without configuration or a selected controller.
	for name, entries := range commandGroups {
		// Explicit and implicit help must expose the same level.
		for _, args := range [][]string{{name}, {name, "--help"}, {name, "-h"}, {"help", name}} {
			output := offlineHelp(t, args...)
			// Every listed child has a runnable grouped spelling.
			for _, entry := range entries {
				// Missing descriptions would hide a supported child command.
				if !strings.Contains(output, entry.summary) {
					t.Fatalf("%v omits %s: %s", args, entry.name, output)
				}
			}
			// Flags and detailed notes remain at the selected leaf.
			if strings.Contains(output, "--revision") || strings.Contains(output, "Full guide:") {
				t.Fatal(output)
			}
		}
	}
}

// TestCommandHelpIsConciseAndOffline preserves complete guide discovery for every leaf.
func TestCommandHelpIsConciseAndOffline(t *testing.T) {
	catalog, err := bundled.List()
	// Missing embedded metadata cannot silently hide a command.
	if err != nil {
		t.Fatal(err)
	}
	commands := []bundled.Skill{}
	// Topic guides have no command usage; TestMinionTopicGuide covers their help.
	for _, item := range catalog {
		// Keep only skills that document a real command.
		if !item.Topic {
			commands = append(commands, item)
		}
	}
	// Every embedded command guide needs a concise usage entry.
	if len(commandHelp) != len(commands) {
		t.Fatalf("%d help entries for %d commands", len(commandHelp), len(commands))
	}
	// Existing direct spellings continue to offer leaf help, except the expanded server group.
	for _, item := range commands {
		// Server now opens its child index; leaf coverage is checked separately.
		if item.Command == "server" {
			continue
		}
		// Both help forms must reach the same command-specific reference.
		t.Run(item.Command, func(t *testing.T) {
			output := offlineHelp(t, item.Command, "--help")
			other := offlineHelp(t, "help", item.Command)
			// Full offline guidance must remain discoverable from concise help.
			if output != other || !strings.Contains(output, "Full guide: hostlier skills show "+item.Command) {
				t.Fatal(output, other)
			}
		})
	}
}

// TestGroupedLeafHelpUsesInvokedPath checks help paths after alias normalization.
func TestGroupedLeafHelpUsesInvokedPath(t *testing.T) {
	// Grouped help must keep the complete runnable command path.
	for _, test := range []struct{ group, action, want string }{
		{"app", "rollback", "hostlier app rollback APP [--revision ID]"},
		{"app", "deploy", "hostlier app deploy [--file FILE]"},
		{"server", "connect", "hostlier server connect https://CONTROLLER"},
		{"server", "use", "hostlier server use PROFILE|[MASTER/]BOX"},
		{"server", "list", "hostlier server list"},
		{"access", "approve", "hostlier access approve CODE --key FILE"},
	} {
		first := offlineHelp(t, test.group, test.action, "--help")
		second := offlineHelp(t, "help", test.group, test.action)
		// Both entry points must show runnable syntax for exactly the requested action.
		if first != second || !strings.Contains(first, test.want) {
			t.Fatal(first, second)
		}
	}
}

// TestHelpAliasesAndUnknownNames preserves aliases and a concise unknown-name response.
func TestHelpAliasesAndUnknownNames(t *testing.T) {
	// Existing singular aliases remain usable after grouping.
	for _, command := range []string{"domain", "client"} {
		output := offlineHelp(t, command, "--help")
		// Alias help must use the canonical runnable spelling.
		if !strings.Contains(output, "hostlier "+command+"s list") {
			t.Fatal(output)
		}
	}
	output := offlineHelp(t, "missing", "--help")
	// Unknown topics must not dump the whole reference.
	if !strings.Contains(output, `Unknown command "missing"`) || strings.Count(output, "\n") != 1 {
		t.Fatal(output)
	}
}

// TestFullDeploymentGuideRemainsOffline preserves complete schema discovery on demand.
func TestFullDeploymentGuideRemainsOffline(t *testing.T) {
	output, err := invoke([]string{"skills", "show", "deploy"}, "", Options{})
	// The detailed schemas stay available without a network or source checkout.
	if err != nil || !strings.Contains(output, "primaryEndpoint: http") || !strings.Contains(output, "protocol: grpc") || !strings.Contains(output, "source: provision") {
		t.Fatalf("%v %s", err, output)
	}
}
