package cli

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ensureField inserts a missing field but never replaces an operator's existing value.
func ensureField(parent *yaml.Node, key string, value *yaml.Node) (*yaml.Node, bool) {
	// YAML mappings store key and value nodes in adjacent positions.
	for i := 0; i < len(parent.Content); i += 2 {
		// Keep the original order, comments and values on repeated enrollment.
		if parent.Content[i].Value == key {
			return parent.Content[i+1], false
		}
	}
	parent.Style &^= yaml.FlowStyle
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	return value, true
}

// mappingField keeps nested scaffold entries compatible with the project parser.
func mappingField(parent *yaml.Node, key string) (*yaml.Node, bool, error) {
	n, added := ensureField(parent, key, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	// Do not erase authored scalars or silently reinterpret incompatible fields.
	if n.Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("%s must be a mapping", key)
	}
	return n, added, nil
}

// scaffoldPackage updates only missing keys for this specific package.
func scaffoldPackage(root *yaml.Node, name string, c *packageContract) (bool, error) {
	packages, _, err := mappingField(root, "packages")
	// A malformed project cannot receive local configuration.
	if err != nil {
		return false, err
	}
	entry, added, err := mappingField(packages, name)
	// Never overwrite an incompatible package entry.
	if err != nil {
		return added, err
	}
	updated, err := c.scaffoldEntry(entry)
	return added || updated, err
}

// scaffoldEntry keeps local config flat and controller references separate.
func (c *packageContract) scaffoldEntry(entry *yaml.Node) (bool, error) {
	var added bool
	config := entry
	var err error
	// Controller settings separate public inputs from secret and output references.
	if c.controller() {
		config, added, err = mappingField(entry, "config")
		// An incompatible authored envelope requires an explicit edit.
		if err != nil {
			return false, err
		}
	}
	updated, err := c.scaffoldInputs(config)
	// Do not write a partial scaffold if any default could not be represented.
	if err != nil {
		return false, err
	}
	added = added || updated
	// Local project hooks receive only their public inputs.
	if !c.controller() {
		return added, nil
	}
	updated, err = c.scaffoldReferences(entry)
	return added || updated, err
}

// scaffoldInputs uses the same compiled property definitions and defaults as planning.
func (c *packageContract) scaffoldInputs(entry *yaml.Node) (bool, error) {
	changed := false
	// Stable ordering makes new projects and subsequent reviews reproducible.
	for _, name := range slices.Sorted(maps.Keys(c.schema.Properties)) {
		field := c.schema.Properties[name]
		raw, err := json.Marshal(field.Default)
		var doc yaml.Node
		// Parse JSON literals as YAML to preserve booleans, numbers and quoted strings.
		if err != nil || yaml.Unmarshal(raw, &doc) != nil {
			return false, fmt.Errorf("invalid default for %s.%s", c.Name, name)
		}
		node := doc.Content[0]
		parts := []string{c.requirementLabel(name)}
		// Expose useful type, choice and help text beside each input.
		if len(field.Types) > 0 {
			parts = append(parts, strings.Join(field.Types, " | "))
		}
		// Display only public metadata from the description.
		if len(field.Enum) > 0 {
			choices, _ := json.Marshal(field.Enum)
			parts = append(parts, string(choices))
		}
		// Keep multiline descriptions inside a single safe YAML comment.
		if field.Description != "" {
			parts = append(parts, strings.Join(strings.Fields(field.Description), " "))
		}
		node.LineComment = strings.Join(parts, "; ")
		_, added := ensureField(entry, name, node)
		changed = changed || added
	}
	return changed, nil
}

// requirementLabel distinguishes unconditional inputs from those needed only in a schema branch.
func (c *packageContract) requirementLabel(name string) string {
	// Root-level requirements apply for every configuration.
	if slices.Contains(c.schema.Required, name) {
		return "required"
	}
	// Either arm of a conditional can introduce a property requirement.
	if c.schema.Then != nil && slices.Contains(c.schema.Then.Required, name) {
		return "conditionally required"
	}
	if c.schema.Else != nil && slices.Contains(c.schema.Else.Required, name) { // The fallback arm is conditional too.
		return "conditionally required"
	}
	return "optional"
}

// scaffoldReferences lists available names without inventing secret values or binding outputs.
func (c *packageContract) scaffoldReferences(entry *yaml.Node) (bool, error) {
	changed := false
	// Only packages declaring secret aliases need a secret reference section.
	if len(c.SecretAliases) > 0 {
		refs, created, err := mappingField(entry, "secrets")
		// Preserve incompatible authored data and report the problem.
		if err != nil {
			return false, err
		}
		changed = created
		// Blank aliases are omitted when composing the step.
		for _, alias := range slices.Sorted(slices.Values(c.SecretAliases)) {
			_, added := ensureField(refs, alias, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null", LineComment: "stored secret name (optional)"})
			changed = changed || added
		}
	}
	names := c.OutputNames()
	// Binding an output is an explicit operator choice.
	if len(names) > 0 {
		bindings, created, err := mappingField(entry, "bindings")
		// Keep authored mappings, including existing application environment names.
		if err != nil {
			return false, err
		}
		// Annotate the new binding map without claiming a value or selecting all outputs.
		if created {
			bindings.LineComment = "ENV: output name; choices: " + strings.Join(names, ", ")
		}
		changed = changed || created
	}
	return changed, nil
}
