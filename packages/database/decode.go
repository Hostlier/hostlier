package database

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// Decode accepts one small strict YAML document and redacts parser diagnostics.
func Decode(r io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(r, 65537))
	// Reject oversized inputs before parsing recursive YAML structures.
	if err != nil || len(data) > 65536 {
		return errors.New("package configuration must be readable and at most 64 KiB")
	}
	var root yaml.Node
	parser := yaml.NewDecoder(bytes.NewReader(data))
	// Raw syntax errors may contain mistakenly pasted secrets.
	if err := parser.Decode(&root); err != nil {
		return errors.New("invalid package YAML")
	}
	// Disallow extra documents, including trailing empty documents.
	if parser.Decode(new(yaml.Node)) != io.EOF {
		return errors.New("package configuration must contain one YAML document")
	}
	// Alias expansion is unnecessary for small dependency configurations.
	if !safeNode(&root, 0) {
		return errors.New("package YAML must be a mapping without aliases or excessive nesting")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	// Unknown fields and duplicate keys are rejected without echoing their contents.
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid package fields or values")
	}
	return nil
}

// safeNode bounds nesting and rejects aliases before decoding into typed structs.
func safeNode(node *yaml.Node, depth int) bool {
	// Root documents must wrap a mapping, never a scalar or sequence.
	if depth == 0 && (len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode) {
		return false
	}
	// Bounded trees prevent recursive alias expansion and excessive parser work.
	if depth > 12 || node.Kind == yaml.AliasNode {
		return false
	}
	// Validate children before the typed decoder traverses them.
	for _, child := range node.Content {
		// A single unsafe subtree invalidates the document.
		if !safeNode(child, depth+1) {
			return false
		}
	}
	return true
}
