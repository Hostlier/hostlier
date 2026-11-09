package controller

import (
	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
	"strconv"
	"strings"
)

// retainedProductPolicy keeps the original approved contract across deployment replacements.
func (c *Controller) retainedProductPolicy(spec model.Project) *workflow.ProductPolicy {
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if app := c.state.Applications[spec.Name]; app != nil && app.ProductPolicy != nil {
		return app.ProductPolicy
	}
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if spec.Workflow != nil {
		return spec.Workflow.ProductPolicy
	}
	return nil
}

// validateProductPolicy enforces both retained and newly declared constraints before package planning.
func (c *Controller) validateProductPolicy(spec model.Project) error {
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if err := checkProductPolicy(spec, c.retainedProductPolicy(spec)); err != nil {
		return err
	}
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if spec.Workflow != nil {
		return checkProductPolicy(spec, spec.Workflow.ProductPolicy)
	}
	return nil
}

// checkProductPolicy rejects omitted resources when a maximum requires an explicit runtime limit.
func checkProductPolicy(spec model.Project, policy *workflow.ProductPolicy) error {
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if policy == nil {
		return nil
	}
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if !policy.Valid() {
		return model.Err("PRODUCT_POLICY", "Invalid product resource policy")
	}
	replicas := model.DesiredReplicas(spec)
	// Delegated maximum is constrained by the retained policy, not just initial capacity.
	if spec.Autoscaling != nil && spec.Autoscaling.MaxReplicas > policy.MaxReplicas {
		return model.Err("PRODUCT_POLICY", "Autoscaling maximum violates retained product policy")
	}
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if replicas > policy.MaxReplicas || ((replicas == 0 || spec.SleepAfter != "") && !policy.AllowZero) {
		return model.Err("PRODUCT_POLICY", "Replica count violates retained product policy")
	}
	return checkProductLimits(spec, policy)
}

// checkProductLimits compares normalized runtime quantities rather than lexical units.
func checkProductLimits(spec model.Project, policy *workflow.ProductPolicy) error {
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if policy.MaxCPU == 0 && policy.MaxMemoryBytes == 0 {
		return nil
	}
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if spec.Resources == nil {
		return model.Err("PRODUCT_POLICY", "Product policy requires explicit resource limits")
	}
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if err := checkProductCPU(spec.Resources.CPU, policy.MaxCPU); err != nil {
		return err
	}
	return checkProductMemory(spec.Resources.Memory, policy.MaxMemoryBytes)
}

// nextProductPolicy accepts an explicitly reviewed replacement; omission retains the prior contract.
func (c *Controller) nextProductPolicy(spec model.Project) *workflow.ProductPolicy {
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if spec.Workflow != nil && spec.Workflow.ProductPolicy != nil {
		return spec.Workflow.ProductPolicy
	}
	return c.retainedProductPolicy(spec)
}

// checkProductCPU requires an explicit positive CPU allocation when bounded.
func checkProductCPU(value string, maximum float64) error {
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if maximum == 0 {
		return nil
	}
	cpu, err := strconv.ParseFloat(value, 64)
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if err != nil || cpu > maximum || cpu <= 0 {
		return model.Err("PRODUCT_POLICY", "CPU exceeds product policy")
	}
	return nil
}

// checkProductMemory requires an explicit positive memory allocation when bounded.
func checkProductMemory(value string, maximum int64) error {
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if maximum == 0 {
		return nil
	}
	memory, err := config.ParseMemory(value)
	// Reject a missing or out-of-policy declaration before runtime mutation.
	if err != nil || memory > maximum || memory <= 0 {
		return model.Err("PRODUCT_POLICY", "Memory exceeds product policy")
	}
	return nil
}

// uncertainProductHook prevents fresh hook keys from bypassing an unresolved prior mutation.
func (c *Controller) uncertainProductHook(app string) bool {
	prefix := c.state.WorkflowNamespace + "/" + app + "/"
	// Hook identities contain an approval suffix inside this application's private namespace.
	for key, record := range c.state.WorkflowRecords {
		// Only an uncertain hook blocks a new lifecycle event; completed dependency reuse is safe.
		if strings.HasPrefix(key, prefix) && strings.Contains(strings.TrimPrefix(key, prefix), "@") && record.Status == "uncertain" {
			return true
		}
	}
	return false
}
