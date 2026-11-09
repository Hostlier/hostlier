// Package skills bundles command-specific coding-agent instructions with the CLI.
package skills

import (
	"embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// content ships skills without requiring the source checkout or network access.
//
//go:embed content/*/SKILL.md
var content embed.FS

// topics are guides for workflows that have no hostlier command of their own.
var topics = map[string]bool{}

// IsTopic reports whether a selector names a guide rather than a CLI command.
func IsTopic(command string) bool { return topics[strings.TrimPrefix(command, "hostlier-")] }

// Skill identifies one independently installable command or topic guide.
type Skill struct {
	Command     string `json:"command" yaml:"-"`
	Topic       bool   `json:"topic,omitempty" yaml:"-"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Content     string `json:"content,omitempty" yaml:"-"`
}

// List reads the embedded catalog in deterministic name order.
func List() ([]Skill, error) {
	entries, err := content.ReadDir("content")
	// A missing embedded catalog is a build packaging error.
	if err != nil {
		return nil, err
	}
	items := make([]Skill, 0, len(entries))
	// Each directory contains a complete standalone skill.
	for _, entry := range entries {
		item, err := read(entry.Name())
		// Reject malformed bundled metadata rather than installing incomplete instructions.
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// read validates metadata against the embedded folder identity.
func read(name string) (Skill, error) {
	var item Skill
	data, err := content.ReadFile("content/" + name + "/SKILL.md")
	// Preserve an embedded filesystem failure for diagnostics.
	if err != nil {
		return item, err
	}
	parts := strings.SplitN(string(data), "---\n", 3)
	// A skill requires bounded YAML frontmatter followed by a nonempty body.
	if len(parts) != 3 || parts[0] != "" || strings.TrimSpace(parts[2]) == "" {
		return item, fmt.Errorf("invalid bundled skill %s", name)
	}
	// Folder names and frontmatter must agree for agent discovery.
	if yaml.Unmarshal([]byte(parts[1]), &item) != nil || item.Name != name || item.Description == "" {
		return item, fmt.Errorf("invalid bundled skill metadata %s", name)
	}
	item.Command = strings.TrimPrefix(name, "hostlier-")
	item.Topic = topics[item.Command]
	item.Content = string(data)
	return item, nil
}

// Select returns all skills when no command is given, otherwise exactly one.
func Select(command string) ([]Skill, error) {
	items, err := List()
	// Empty selection is the documented install-all behavior.
	if err != nil || command == "" {
		return items, err
	}
	command = strings.TrimPrefix(command, "hostlier-")
	aliases := map[string]string{"domain": "domains", "client": "clients"}
	// Accept the same command aliases as the CLI.
	if alias := aliases[command]; alias != "" {
		command = alias
	}
	// Match against the catalog rather than interpreting input as a filesystem path.
	for _, item := range items {
		// Only the requested command is installed, with no implicit dependencies.
		if item.Command == command {
			return []Skill{item}, nil
		}
	}
	return nil, fmt.Errorf("unknown skill command %q; run hostlier skills list", command)
}
