package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"hostlier/internal/model"
	"strings"

	"gopkg.in/yaml.v3"
)

// SecretSource is local input kept separate from the serializable deployment specification.
type SecretSource struct {
	Name    string  `json:"name"`
	Value   *string `json:"-"`
	FromEnv string  `json:"-"`
}

// ProjectInput keeps private local sources out of every plan and project serialization.
type ProjectInput struct {
	Project model.Project  `json:"project"`
	Secrets []SecretSource `json:"-"`
}

// ReadProjectSources lowers explicit local secret sources before strict model decoding.
func ReadProjectSources(path, namespace string) (ProjectInput, error) {
	var input ProjectInput
	var tree yaml.Node
	// Reuse bounded, single-document and alias-free YAML loading.
	if err := readYAML(path, &tree); err != nil {
		return input, err
	}
	// An empty document cannot identify an application or its secret namespace.
	if len(tree.Content) != 1 {
		return input, invalid("Expected a project document")
	}
	root := tree.Content[0]
	// Attached sidecars supply their parent namespace explicitly.
	if namespace == "" {
		namespace = mappingString(root, "name")
	}
	// Replace only secret declarations; ordinary model validation remains authoritative.
	if err := lowerProjectSecrets(root, namespace, &input.Secrets); err != nil {
		return input, err
	}
	data, err := yaml.Marshal(&tree)
	// Encoding errors cannot return a partially lowered deployment.
	if err != nil {
		return input, invalid("Cannot decode project configuration")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	// Keep unknown fields, duplicate keys and malformed values strict after lowering.
	if err = decoder.Decode(&input.Project); err != nil {
		return input, invalid("Invalid configuration: " + safeYAMLError(err))
	}
	return input, uniqueSecretSources(input.Secrets)
}

// mappingString reads an optional scalar without decoding or interpolating arbitrary YAML.
func mappingString(n *yaml.Node, key string) string {
	// Traverse only actual mapping pairs; typed decoding handles other shapes.
	for i := 0; n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		// Namespace names are never inferred from another field.
		if n.Content[i].Value == key {
			return n.Content[i+1].Value
		}
	}
	return ""
}

// lowerProjectSecrets recursively replaces private sources with stable controller references.
func lowerProjectSecrets(n *yaml.Node, namespace string, sources *[]SecretSource) error {
	// Restrict traversal to project fields rather than similarly named arbitrary config data.
	for i := 0; n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		field, value := n.Content[i].Value, n.Content[i+1]
		// Secret maps and sidecar projects are the only locally expanded fields.
		switch field {
		// Lower only the workload secret bindings.
		case "secrets":
			// Propagate invalid or ambiguous secret declarations before any upload.
			if err := lowerSecretBindings(value, namespace, sources); err != nil {
				return err
			}
		// Members receive independent namespaces under the same application.
		case "sidecars":
			// Each sidecar key forms part of its stable secret identity.
			for j := 0; value.Kind == yaml.MappingNode && j+1 < len(value.Content); j += 2 {
				// All member errors abort the complete project snapshot.
				if err := lowerProjectSecrets(value.Content[j+1], namespace+"/"+value.Content[j].Value, sources); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// lowerSecretBindings lowers flat literals, environment variables and explicit stored references.
func lowerSecretBindings(n *yaml.Node, namespace string, sources *[]SecretSource) error {
	// Model decoding reports non-map secrets using its normal safe schema error.
	for i := 0; n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i].Value, n.Content[i+1]
		source, reference, err := decodeSecretSource(value)
		// Invalid sources must fail before any controller request.
		if err != nil {
			return err
		}
		// Stored references contain no local value and require no upload.
		if reference != "" {
			*value = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: reference}
			continue
		}
		// Local values belong to this application and component.
		source.Name = generatedSecretName(namespace, key)
		*sources = append(*sources, source)
		*value = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: source.Name}
	}
	return nil
}

// generatedSecretName derives identity only from public names, never from secret bytes.
func generatedSecretName(namespace, key string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + key))
	prefix := strings.ReplaceAll(strings.ToLower(namespace+"-"+key), "/", "-")
	// Bound generated names even when environment or component identifiers are long.
	if len(prefix) > 60 {
		prefix = prefix[:60]
	}
	return fmt.Sprintf("hl-%s-%x", prefix, sum[:10])
}

// uniqueSecretSources rejects conflicting writes before publishing any value.
func uniqueSecretSources(sources []SecretSource) error {
	seen := map[string]bool{}
	// A controller name may have only one source in an atomic project input.
	for _, source := range sources {
		// Duplicate declarations must not depend on YAML or map iteration order.
		if seen[source.Name] {
			return invalid("Each declared secret name must have exactly one value source")
		}
		seen[source.Name] = true
	}
	return nil
}
