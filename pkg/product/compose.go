package product

import (
	"encoding/json"
	"errors"
	"hostlier/pkg/workflow"
	"reflect"
	"regexp"
)

// publicName restricts local product and input identities to portable declaration names.
var publicName = regexp.MustCompile(`^[a-z][a-zA-Z0-9_-]{0,62}$`)

// Compose resolves explicit typed bindings into the ordinary reviewed deployment workflow.
func Compose(d Definition, invocation Invocation) (workflow.Spec, error) {
	// Bound the schema before allocation or workflow expansion.
	if d.Version != 1 || !publicName.MatchString(d.Name) || d.Revision == "" || len(d.Revision) > 64 || len(d.Inputs) > 64 {
		return workflow.Spec{}, errors.New("invalid product identity or version")
	}
	values, err := resolveInputs(d.Inputs, invocation.Inputs)
	// Invalid public values cannot enter executable package configuration.
	if err != nil {
		return workflow.Spec{}, err
	}
	secrets, err := resolveSecrets(d.Secrets, invocation.SecretRefs)
	// Only explicitly declared stored secret references may reach a package.
	if err != nil {
		return workflow.Spec{}, err
	}
	result := workflow.Spec{Bindings: d.Bindings, ProductPolicy: d.Policy, Product: &workflow.ProductIdentity{Name: d.Name, Revision: d.Revision}}
	// Expand each declaration independently into a detached package step.
	for _, step := range d.Steps {
		expanded, err := expandStep(step, values, secrets)
		// Reject references before returning any partially composed graph.
		if err != nil {
			return workflow.Spec{}, err
		}
		result.Steps = append(result.Steps, expanded)
	}
	return result, result.Validate()
}

// resolveInputs rejects unknown inputs and checks defaults with the same type rules as supplied values.
func resolveInputs(schema map[string]Input, supplied map[string]any) (map[string]any, error) {
	result := map[string]any{}
	// Unknown input values are almost always declaration mistakes.
	for key := range supplied {
		// Supplied keys must belong to the declared public schema.
		if _, ok := schema[key]; !ok {
			return nil, errors.New("unknown product input")
		}
	}
	// Every input declaration is validated even when no value is supplied.
	for key, input := range schema {
		value, ok := supplied[key]
		// Defaults are public configuration and never read from the environment.
		if !ok {
			value = input.Default
		}
		// Required values and invalid schemas fail before composition.
		if !publicName.MatchString(key) || !validInput(input, value) {
			return nil, errors.New("invalid or missing product input")
		}
		// Omitted optional values do not override a package's public defaults.
		if value != nil {
			result[key] = value
		}
	}
	return result, nil
}

// validInput validates supported scalar types and bounded enum restrictions.
func validInput(input Input, value any) bool {
	// Reject unsupported types even for omitted optional values.
	if input.Type != "string" && input.Type != "integer" && input.Type != "boolean" {
		return false
	}
	// Only optional omitted values may remain unresolved.
	if value == nil {
		return !input.Required
	}
	// Enforce scalar type before checking an optional enum.
	if !scalarType(input.Type, value) {
		return false
	}
	// An empty enum permits any scalar of the declared type.
	if len(input.Enum) == 0 {
		return true
	}
	// Enum values are explicit public constants.
	for _, allowed := range input.Enum {
		// Accept an explicitly listed value without string or numeric coercion.
		if reflect.DeepEqual(value, allowed) {
			return true
		}
	}
	return false
}

// scalarType recognizes JSON/YAML-compatible scalar values without coercing strings.
func scalarType(kind string, value any) bool {
	// Type-directed checks keep secrets and composite values out of public input substitution.
	switch kind {
	// Strings remain literal public configuration.
	case "string":
		_, ok := value.(string)
		return ok
	// Boolean inputs require native scalar values.
	case "boolean":
		_, ok := value.(bool)
		return ok
	// Both supported document encodings represent integer values.
	case "integer":
		return integerValue(value)
	}
	return false
}

// integerValue accepts native YAML integers and integral JSON numbers.
func integerValue(value any) bool {
	// Both serialization formats have explicit integral representations.
	switch v := value.(type) {
	// Native YAML integer representations need no conversion.
	case int, int64, int32, uint, uint64:
		return true
	// JSON numbers must be integral and exactly representable.
	case float64:
		return v >= -9007199254740991 && v <= 9007199254740991 && v == float64(int64(v))
	}
	return false
}

// resolveSecrets preserves only declared stored references and rejects undeclared aliases.
func resolveSecrets(declared []string, supplied map[string]string) (map[string]string, error) {
	allowed := map[string]bool{}
	// Duplicate or invalid declarations are ambiguous authoring errors.
	for _, key := range declared {
		// Each private alias has exactly one portable declaration.
		if !publicName.MatchString(key) || allowed[key] {
			return nil, errors.New("invalid product secret declaration")
		}
		allowed[key] = true
	}
	// All declared secret inputs are required stored-secret names.
	for key := range allowed {
		// Required aliases must resolve to a stored secret name.
		if supplied[key] == "" {
			return nil, errors.New("missing product secret reference")
		}
	}
	// Caller-provided aliases may never invent a new package secret binding.
	for key := range supplied {
		// Undeclared values cannot expand package authority.
		if !allowed[key] {
			return nil, errors.New("unknown product secret reference")
		}
	}
	return supplied, nil
}

// expandStep copies a package declaration before substituting explicit approved inputs.
func expandStep(s Step, values map[string]any, secrets map[string]string) (workflow.Step, error) {
	raw, err := json.Marshal(s.Step)
	// Only JSON-compatible public configuration belongs in an executable request.
	if err != nil {
		return workflow.Step{}, errors.New("invalid product step")
	}
	var result workflow.Step
	// JSON round-trip prevents composition from mutating the reusable definition.
	if json.Unmarshal(raw, &result) != nil {
		return result, errors.New("invalid product step")
	}
	result.Config = nonNilConfig(result.Config)
	result.Secrets = nonNilSecrets(result.Secrets)
	// Public values bind only explicitly named top-level configuration fields.
	for field, input := range s.ConfigInputs {
		value, ok := values[input]
		// Missing references and duplicate constant/bound settings are rejected.
		if !ok || result.Config[field] != nil {
			return result, errors.New("invalid product configuration input")
		}
		result.Config[field] = value
	}
	// Stored references never pass through public input values.
	for alias, input := range s.SecretInputs {
		value, ok := secrets[input]
		// A declaration may not overwrite an existing private alias.
		if !ok || result.Secrets[alias] != "" {
			return result, errors.New("invalid product secret input")
		}
		result.Secrets[alias] = value
	}
	return result, nil
}

// nonNilConfig gives substitution a detached writable map.
func nonNilConfig(value map[string]any) map[string]any {
	// Allocate only when the declaration omitted this optional map.
	if value == nil {
		return map[string]any{}
	}
	return value
}

// nonNilSecrets gives secret-reference binding a detached writable map.
func nonNilSecrets(value map[string]string) map[string]string {
	// Allocate only when the declaration omitted this optional map.
	if value == nil {
		return map[string]string{}
	}
	return value
}
