package cli

import (
	"fmt"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/workflow"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
)

// enrolledPackages owns local executable snapshots and their public proposals for one CLI operation.
type enrolledPackages struct {
	hooks         map[string]*packageexec.ProjectHooks
	builder       string
	contributions map[string]packagekit.ProjectContribution
}

// loadEnrollment opens only named local binaries and snapshots them for the entire invocation.
func (s *session) loadEnrollment(primary string, authored *config.ProjectInput) (*enrolledPackages, error) {
	group := &enrolledPackages{hooks: map[string]*packageexec.ProjectHooks{}, builder: primary, contributions: map[string]packagekit.ProjectContribution{}}
	names := map[string]bool{}
	// Flag-only projects may have no deployment document yet.
	if authored != nil {
		// Only explicit project entries authorize package participation.
		for name := range authored.Project.Packages {
			names[name] = true
		}
	}
	// The legacy single-package selector remains an explicit enrollment.
	if primary != "" {
		names[primary] = true
	}
	// Stable order makes errors and review output reproducible.
	for _, name := range slices.Sorted(maps.Keys(names)) {
		// A partially opened group must release every acquired snapshot.
		var settings map[string]any
		// Flag-only enrollments use schema defaults.
		if authored != nil {
			settings = authored.Project.Packages[name]
		}
		// Release every opened snapshot if any contract fails validation.
		if err := s.openEnrolled(group, name, settings); err != nil {
			group.close()
			return nil, err
		}
	}
	return group, nil
}

// openEnrolled chooses at most one builder and records independently composable contributors.
func (s *session) openEnrolled(g *enrolledPackages, name string, settings map[string]any) error {
	runner, err := s.localPackageRunner(name)
	// An absent enrollment must be installed deliberately by the operator.
	if err != nil {
		return err
	}
	h, err := runner.OpenProject(s.ctx, name, filepath.Dir(s.projectPath()))
	// Discovery and invocation must use the same executable snapshot.
	if err != nil {
		return err
	}
	g.hooks[name] = h
	c, err := compileContract(h.Capability())
	// Parse only the metadata from the executable that will be used for this plan.
	if err != nil {
		return err
	}
	// A controller enrollment composes a normal reviewed step; run no local lifecycle code.
	if c.controller() {
		contribution, err := c.contribution(settings)
		g.contributions[name] = contribution
		return err
	}
	// Legacy hooks with no declared config keep their existing request shape.
	if len(c.ConfigSchema) > 0 {
		values, err := c.inputs(settings)
		// Required or invalid inputs stop the entire group before any hook executes.
		if err != nil {
			return err
		}
		h.SetConfig(values)
	}
	return g.selectLocal(name, h)
}

// selectLocal selects one artifact builder and any number of local contributors.
func (g *enrolledPackages) selectLocal(name string, h *packageexec.ProjectHooks) error {
	builds := h.Has("project-plan") || h.Has("project-package")
	// Selecting a primary artifact is exclusive independently of enrollment order.
	if builds && g.builder != "" && g.builder != name {
		return fmt.Errorf("packages %q and %q both claim the primary workload; enroll one builder", g.builder, name)
	}
	// A planner-only builder can supply an existing registry workload.
	if builds {
		g.builder = name
	}
	// Contributions compose even when the same package also builds the primary.
	if h.Has("project-contribute") {
		g.contributions[name] = packagekit.ProjectContribution{}
		return nil
	}
	// Controller-only resource packages are selected through workflow steps.
	if !builds {
		return fmt.Errorf("enrolled package %q has no local deployment hook; declare controller resource packages in workflow.steps", name)
	}
	return nil
}

// close releases private snapshots after planning, deployment or failure.
func (g *enrolledPackages) close() {
	// Include contributors that never produced an artifact.
	for _, h := range g.hooks {
		h.Close()
	}
}

// collect snapshots reviewed contributions; after build it verifies the same packages still propose identical resources.
func (s *session) collect(g *enrolledPackages, arch string, verify bool) error {
	// A standalone plan and a deploy collect in the same order.
	for _, name := range slices.Sorted(maps.Keys(g.contributions)) {
		// Static controller steps are already captured in the project proposal.
		if !g.hooks[name].Has("project-contribute") {
			continue
		}
		c, err := g.hooks[name].Contribute(s.ctx, arch)
		// A failed contributor invalidates the whole deployment proposal.
		if err != nil {
			return err
		}
		// A changed contribution must go through a new review.
		if verify && !reflect.DeepEqual(c, g.contributions[name]) {
			return fmt.Errorf("package %q contributions changed during packaging; review a new plan", name)
		}
		g.contributions[name] = c
	}
	return nil
}

// provided returns only binding names. Connection strings and credentials never enter build inputs.
func (g *enrolledPackages) provided() []string {
	names := map[string]bool{}
	// Combine names across all contributors, including the primary package.
	for _, c := range g.contributions {
		// Builders may require an auxiliary service credential as well as its URL.
		for k := range c.Secrets {
			names[k] = true
		}
		// Native resources bind outputs directly to the application.
		for k := range c.Bindings {
			names[k] = true
		}
		// Workflow bindings can also satisfy declared build requirements.
		for k := range c.WorkflowBindings {
			names[k] = true
		}
	}
	return slices.Sorted(maps.Keys(names))
}

// mergeContributions keeps one namespace per Hostlier resource and stable review order across invocations.
func mergeContributions(p *model.Project, contributions map[string]packagekit.ProjectContribution) error {
	owners := map[string]string{}
	// Deterministic merging reports the owners of conflicting declarations.
	for _, name := range slices.Sorted(maps.Keys(contributions)) {
		c := contributions[name]
		// Sidecars and secret references are additive; no package may replace an authored member.
		if err := mergePackageMap(&p.Sidecars, c.Sidecars, owners, "sidecars", name); err != nil {
			return err
		}
		// Only stored secret names enter the public proposal, never resolved credentials.
		if err := mergePackageMap(&p.Secrets, c.Secrets, owners, "secrets", name); err != nil {
			return err
		}
		// Different settings for one resource may change its ownership or persistence.
		if err := mergePackageMap(&p.Dependencies, c.Dependencies, owners, "dependencies", name); err != nil {
			return err
		}
		// Each variable must resolve to one resource output.
		if err := mergePackageMap(&p.Bindings, c.Bindings, owners, "bindings", name); err != nil {
			return err
		}
		// Workflow additions are checked before the complete graph is validated.
		if err := mergeWorkflowContribution(p, c, owners, name); err != nil {
			return err
		}
	}
	return nil
}

// mergePackageMap coalesces equal claims and rejects conflicts without exposing either value.
func mergePackageMap[T any](dst *map[string]T, src map[string]T, owners map[string]string, section, name string) error {
	// Omitted maps should remain omitted in the deployment plan.
	if len(src) == 0 {
		return nil
	}
	// Allocate an owned destination only when a contributor adds entries.
	if *dst == nil {
		*dst = map[string]T{}
	}
	// Stable key order gives a deterministic first conflict.
	for _, k := range slices.Sorted(maps.Keys(src)) {
		key := section + "." + k
		// Repeated identical requests describe one deployment effect.
		if v, exists := (*dst)[k]; exists {
			// Never print credential-bearing values in a conflict diagnostic.
			if !reflect.DeepEqual(v, src[k]) {
				return fmt.Errorf("conflicting %s from %s and %s", key, defaultValue(owners[key], "primary project"), name)
			}
			continue
		}
		(*dst)[k] = src[k]
		owners[key] = name
	}
	return nil
}

// mergeWorkflowContribution preserves authored step order and permits explicit needs across package boundaries.
func mergeWorkflowContribution(p *model.Project, c packagekit.ProjectContribution, owners map[string]string, name string) error {
	// Resource-only contributors need no deployment workflow.
	if len(c.Steps)+len(c.WorkflowBindings) == 0 {
		return nil
	}
	// Create a workflow only when a package contributes work to it.
	if p.Workflow == nil {
		p.Workflow = &workflow.Spec{}
	}
	index := map[string]workflow.Step{}
	// Authored IDs and previous contributors already own their step names.
	for _, step := range p.Workflow.Steps {
		index[step.ID] = step
	}
	// Preserve each package’s declared step order and needs graph.
	for _, step := range c.Steps {
		_, exists := index[step.ID]
		// Conflicting step definitions must fail before calling a controller.
		if err := mergePackageMap(&index, map[string]workflow.Step{step.ID: step}, owners, "steps", name); err != nil {
			return err
		}
		// Coalesced identical steps execute only once.
		if !exists {
			p.Workflow.Steps = append(p.Workflow.Steps, step)
		}
	}
	return mergePackageMap(&p.Workflow.Bindings, c.WorkflowBindings, owners, "workflow.bindings", name)
}
