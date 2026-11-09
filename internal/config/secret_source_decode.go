package config

import (
	"gopkg.in/yaml.v3"
	"strings"
)

// decodeSecretSource interprets only explicit flat syntax, without shell expansion or remote lookup.
func decodeSecretSource(n *yaml.Node) (SecretSource, string, error) {
	var source SecretSource
	// Numeric and structured values need an explicit string representation.
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return source, "", invalid("Secret bindings must be strings: a literal, $VARIABLE, or secret://NAME")
	}
	value := n.Value
	// Existing controller values use explicit references, never existence-based guessing.
	if strings.HasPrefix(value, "secret://") {
		name := strings.TrimPrefix(value, "secret://")
		// Reject malformed references without exposing their input bytes.
		if !secretRE.MatchString(name) {
			return source, "", invalid("Invalid stored secret reference")
		}
		return source, name, nil
	}
	// Double dollar escapes a leading dollar without evaluating the remainder.
	if strings.HasPrefix(value, "$$") {
		value = value[1:]
		// A single leading dollar selects an exact local environment name.
	} else if strings.HasPrefix(value, "$") {
		source.FromEnv = value[1:]
		// Resolve exactly one local environment name; no interpolation or commands are supported.
		if !envRE.MatchString(source.FromEnv) {
			return source, "", invalid("Secret environment sources must use $VARIABLE; use $$ for a literal dollar")
		}
		return source, "", nil
	}
	// Preserve whitespace and bound private bytes before constructing an upload.
	if len(value) == 0 || len(value) > 65536 {
		return source, "", invalid("Secret values must contain 1–65536 bytes")
	}
	source.Value = &value
	return source, "", nil
}
