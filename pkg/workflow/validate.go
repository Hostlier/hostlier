package workflow

import (
	"errors"
	"regexp"
	"strings"
)

var name = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var field = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
var secretName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Validate rejects ambiguous identities, invalid references, cycles and unbounded graphs.
func (s Spec) Validate() error {
	// Workflows are intentionally bounded for review and sequential execution.
	if len(s.Steps) == 0 || len(s.Steps) > 32 || len(s.Bindings) > 128 {
		return errors.New("workflow requires 1–32 steps and at most 128 bindings")
	}
	seen := map[string]bool{}
	// Establish identities before checking graph edges.
	for _, step := range s.Steps {
		// Each step must be a valid unique package invocation.
		if seen[step.ID] || validateStep(step) != nil {
			return errors.New("invalid or duplicate workflow step")
		}
		seen[step.ID] = true
	}
	// Validate graph references and explicit dependency declarations.
	for _, step := range s.Steps {
		// Every input producer must be a declared dependency.
		if err := validateEdges(step, seen); err != nil {
			return err
		}
	}
	// Validate application outputs separately from dependency graph ordering.
	if err := validateBindings(s.Bindings, seen); err != nil {
		return err
	}
	return validateProductOrder(s)
}

// validateBindings checks application output references without resolving private values.
func validateBindings(bindings map[string]string, seen map[string]bool) error {
	// Application bindings may only select a known producer and valid output key.
	for env, ref := range bindings {
		id, output, ok := strings.Cut(ref, ".")
		// Bindings must be structurally unambiguous.
		if !field.MatchString(env) || !ok || !seen[id] || !field.MatchString(output) {
			return errors.New("invalid workflow binding")
		}
	}
	return nil
}

// validateStep prevents arbitrary process execution and limits private input references.
func validateStep(s Step) error {
	// Lifecycle positions are bounded independently of package configuration.
	if !validHook(s) {
		return errors.New("invalid controller package hook")
	}
	// All steps, including the deploy hook, retain the existing lifecycle restrictions.
	if !validPhase(s.Phase) || !validLifecycle(s) {
		return errors.New("invalid lifecycle phase")
	}
	// Package IDs are portable names, not executable paths.
	if !name.MatchString(s.ID) || !name.MatchString(s.Package) || len(s.Secrets) > 32 || len(s.Inputs) > 32 || len(s.Needs) > 32 {
		return errors.New("invalid workflow step")
	}
	return validateStepSecrets(s)
}

// validHook restricts controller-side entry points to the reviewed pre-rollout phase.
func validHook(s Step) bool {
	return s.Hook == "" || (s.Hook == ProjectDeployHook && s.Phase == "beforeDeploy")
}

// validateStepSecrets checks private aliases without widening lifecycle authority.
func validateStepSecrets(s Step) error {
	// A private input cannot be supplied by both a stored secret and another step.
	for alias, ref := range s.Secrets {
		// Validate references without retrieving their values.
		if !field.MatchString(alias) || !secretName.MatchString(ref) || s.Inputs[alias] != "" {
			return errors.New("invalid workflow secret reference")
		}
	}
	return nil
}

// validateEdges checks declared ordering and private data dependencies.
func validateEdges(s Step, ids map[string]bool) error {
	needs := map[string]bool{}
	// Reject missing, self-referential or duplicate dependency edges.
	for _, id := range s.Needs {
		// Every dependency must identify a different declared step exactly once.
		if id == s.ID || !ids[id] || needs[id] {
			return errors.New("invalid workflow dependency")
		}
		needs[id] = true
	}
	// Input references are explicit edges, never template interpolation or shell expansion.
	for alias, ref := range s.Inputs {
		id, output, ok := strings.Cut(ref, ".")
		// Only direct declared predecessors may provide private inputs.
		if !field.MatchString(alias) || !ok || !needs[id] || !field.MatchString(output) {
			return errors.New("workflow input requires a declared producer")
		}
	}
	return nil
}

// Order uses stable declaration order to break ties between otherwise independent steps.
func (s Spec) Order() ([]Step, error) {
	done := map[string]bool{}
	ordered := []Step{}
	// Each pass must resolve at least one dependency or the graph is invalid.
	for len(ordered) < len(s.Steps) {
		before := len(ordered)
		// Select ready steps in declaration order.
		for _, step := range s.Steps {
			// A resolved step is emitted exactly once.
			if !done[step.ID] && ready(step.Needs, done) {
				ordered = append(ordered, step)
				done[step.ID] = true
			}
		}
		// No progress means a cycle or unresolved identity.
		if len(ordered) == before {
			return nil, errors.New("workflow contains a cycle or missing dependency")
		}
	}
	return ordered, nil
}

// ready checks whether all predecessor results are available.
func ready(needs []string, done map[string]bool) bool {
	// One unfinished predecessor blocks execution.
	for _, id := range needs {
		// Resolve every edge before proceeding.
		if !done[id] {
			return false
		}
	}
	return true
}

// ValidOutput restricts returned connection fields to bounded private string values.
func ValidOutput(values map[string]string) bool {
	// Bound allocation and prevent empty success responses.
	if len(values) == 0 || len(values) > 32 {
		return false
	}
	// Every output must be safe to bind into an environment.
	for key, value := range values {
		// NUL cannot be represented in a process environment.
		if !field.MatchString(key) || len(value) == 0 || len(value) > 65536 || strings.ContainsRune(value, 0) {
			return false
		}
	}
	return true
}

// validLifecycle distinguishes retained dependencies from explicitly reviewed same-owner updates.
func validLifecycle(s Step) bool {
	return s.Lifecycle == "" || (s.Lifecycle == "managed" && s.Phase == "")
}
