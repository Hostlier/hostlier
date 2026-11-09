package cli

import (
	"fmt"
	"strings"

	bundled "hostlier/internal/skills"
)

// topLevelHelp shows only the command domains, like the Fused CLI root help.
func topLevelHelp() string {
	return commandIndex("hostlier", "Hostlier — deploy and manage applications", rootCommands)
}

// showCommandHelp resolves command groups and leaf help without contacting a controller.
func (s *session) showCommandHelp() bool {
	command := s.command
	// Explicit help shares the same routing as COMMAND --help.
	if command == "help" {
		command = s.sub
	}
	// Empty help requests use the root index.
	if command == "" {
		return false
	}
	// Group help exposes only immediate children, never all of their options.
	if entries, ok := commandGroups[command]; ok && s.parsed.commandPath == "" {
		s.message(commandIndex("hostlier "+command, groupSummary(command), entries))
		return true
	}
	items, err := bundled.Select(command)
	// Unknown help requests remain short and offer the next valid discovery step.
	if err != nil {
		s.message(fmt.Sprintf("Unknown command %q. Run hostlier --help for commands.", command))
		return true
	}
	s.message(s.leafHelp(items[0]))
	return true
}

// leafHelp retains the invoked command path while linking to its existing offline guide.
func (s *session) leafHelp(item bundled.Skill) string {
	// Topic guides have no usage of their own; point at the guide instead.
	if item.Topic {
		return fmt.Sprintf("%s\n\n%s is a guide, not a command.\n\nRead it:     hostlier skills show %s\nInstall it:  hostlier skills install %s", item.Description, item.Command, item.Command, item.Command)
	}
	help := commandHelp[item.Command]
	path := defaultValue(s.parsed.commandPath, item.Command)
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n\nUsage:\n", item.Description)
	// Show only usage belonging to the selected leaf action.
	for _, usage := range helpUsage(item.Command, path, help.usage) {
		fmt.Fprintf(&out, "  hostlier %s\n", usage)
	}
	// Operational notes belong at the leaf, after its syntax.
	if help.note != "" {
		fmt.Fprintf(&out, "\n%s\n", help.note)
	}
	fmt.Fprintf(&out, "\nFull guide: hostlier skills show %s", item.Command)
	return out.String()
}

// helpUsage substitutes grouped aliases without duplicating server list/use positionals.
func helpUsage(command, path, usage string) []string {
	lines := strings.Split(usage, "\n")
	// Native server profile commands are already nested in their canonical usage.
	if command == "server" && path != command {
		return []string{findUsage(lines, path)}
	}
	// Every other group is a prefix on the existing command spelling.
	for i, line := range lines {
		lines[i] = path + strings.TrimPrefix(line, command)
	}
	return lines
}

// findUsage selects one native subcommand from a multi-action help entry.
func findUsage(lines []string, path string) string {
	// Match the selected action without including neighboring actions.
	for _, line := range lines {
		// A complete command token boundary avoids prefix collisions.
		if line == path || strings.HasPrefix(line, path+" ") {
			return line
		}
	}
	return path
}
