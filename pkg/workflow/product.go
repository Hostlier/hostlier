package workflow

import (
	"errors"
	"math"
	"strings"
)

// ProductPolicy is captured by the server on the first successful product deployment.
// Subsequent operations must satisfy the retained constraints, even without a workflow.
type ProductPolicy struct {
	MaxReplicas    int     `json:"maxReplicas" yaml:"maxReplicas"`
	AllowZero      bool    `json:"allowZero,omitempty" yaml:"allowZero,omitempty"`
	MaxCPU         float64 `json:"maxCPU,omitempty" yaml:"maxCPU,omitempty"`
	MaxMemoryBytes int64   `json:"maxMemoryBytes,omitempty" yaml:"maxMemoryBytes,omitempty"`
}

// validPhase permits only the two bounded deployment lifecycle positions.
func validPhase(phase string) bool {
	return phase == "" || phase == "beforeDeploy" || phase == "afterSuccess"
}

// validatePhases prevents startup dependencies from depending on effects that occur after readiness.
func validatePhases(spec Spec) error {
	phases := map[string]string{}
	// Inspect only declared product graph entries.
	for _, step := range spec.Steps {
		phases[step.ID] = step.Phase
	}
	// Inspect only declared product graph entries.
	for _, step := range spec.Steps {
		// Reject a missing or out-of-policy declaration before runtime mutation.
		if step.Phase == "afterSuccess" {
			continue
		}
		// Inspect only declared product graph entries.
		for _, need := range step.Needs {
			// Reject a missing or out-of-policy declaration before runtime mutation.
			if phases[need] == "afterSuccess" {
				return errors.New("startup step cannot depend on after-success hook")
			}
		}
	}
	// Inspect only declared product graph entries.
	for _, reference := range spec.Bindings {
		id, _, _ := strings.Cut(reference, ".")
		// Reject a missing or out-of-policy declaration before runtime mutation.
		if phases[id] == "afterSuccess" {
			return errors.New("application cannot bind after-success hook outputs")
		}
	}
	return nil
}

// validateProductOrder checks phase boundaries before sorting the dependency graph.
func validateProductOrder(spec Spec) error {
	// Policy declarations must validate during offline composition as well as server review.
	if spec.ProductPolicy != nil && !spec.ProductPolicy.Valid() {
		return errors.New("invalid product resource policy")
	}
	// Product release metadata is bounded but never supplies executable authority.
	if spec.Product != nil && (spec.Product.Name == "" || len(spec.Product.Name) > 63 || spec.Product.Revision == "" || len(spec.Product.Revision) > 64) {
		return errors.New("invalid product release identity")
	}
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if err := validatePhases(spec); err != nil {
		return err
	}
	_, err := spec.Order()
	return err
}

// ProductIdentity retains the authored product release in reviewed and rollback specifications.
type ProductIdentity struct {
	Name     string `json:"name" yaml:"name"`
	Revision string `json:"revision" yaml:"revision"`
}

// Valid rejects unbounded replica maxima and non-finite or negative resource limits.
func (p ProductPolicy) Valid() bool {
	return p.MaxReplicas >= 1 && p.MaxReplicas <= 100 && p.MaxCPU >= 0 && !math.IsNaN(p.MaxCPU) && !math.IsInf(p.MaxCPU, 0) && p.MaxMemoryBytes >= 0
}
