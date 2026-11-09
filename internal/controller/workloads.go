package controller

import (
	"context"
	"hostlier/internal/model"
	"net/http"
	"strings"
)

// pinWorkload resolves every member before a reviewed group revision is created.
func (c *Controller) pinWorkload(r *http.Request, p model.Project) (model.Project, []string, error) {
	info, err := c.opts.Runtime.InspectImage(r.Context(), p.Image)
	// A mutable or unavailable primary prevents group approval.
	if err != nil {
		return p, nil, err
	}
	p, err = immutableImage(p, info.Digest)
	// Reject noncanonical image resolution before inspecting additional members.
	if err != nil {
		return p, nil, err
	}
	warnings := info.Warnings
	// Each sidecar is an equally immutable part of the revision.
	for _, name := range model.MemberNames(p) {
		member, notes, e := c.pinWorkload(r, p.Sidecars[name])
		// Never approve a partially resolved group.
		if e != nil {
			return p, nil, e
		}
		p.Sidecars[name] = member
		warnings = append(warnings, notes...)
	}
	return p, warnings, nil
}

// validateWorkloadReferences rejects undeclared or unauthorized private endpoints.
func (c *Controller) validateWorkloadReferences(p model.Project) error {
	// Existing consumers keep their approved access contract until stopped.
	if err := c.validateProviderChange(p); err != nil {
		return err
	}
	// Loopback publication only makes sense inside a parent group.
	if p.Endpoint != nil && p.Endpoint.Exposure == "loopback" {
		return model.Err("INVALID_CONFIG", "Loopback endpoints must be sidecars")
	}
	// Resolve every declared endpoint before starting any infrastructure work.
	for _, ref := range model.EnvironmentBindings(p) {
		parts := strings.Split(ref, ".")
		// Database bindings have their own resource contract.
		if len(parts) != 3 {
			continue
		}
		// Provider access is checked per endpoint, independently of database bindings.
		if err := c.validateEndpointReference(p, parts); err != nil {
			return err
		}

	}
	return nil
}

// allowsConsumer checks the provider-owned private access declaration.
func allowsConsumer(p model.Project, name string) bool {
	// Public and listenerless services do not authorize private attachments.
	if p.Endpoint == nil || p.Endpoint.Exposure != "private" {
		return false
	}
	// Authorization belongs to the provider, not the caller's binding.
	for _, consumer := range p.Endpoint.Consumers {
		// Match canonical application identity exactly.
		if consumer == name {
			return true
		}
	}
	return false
}

// componentLogger is implemented by backends that isolate per-member diagnostics.
type componentLogger interface {
	ComponentLogs(context.Context, string, string) (string, error)
}

// readComponentLogs keeps component selection explicit and backend-scoped.
func (c *Controller) readComponentLogs(r *http.Request, name string) (string, error) {
	component := r.URL.Query().Get("component")
	// The default log target remains the primary application.
	if component == "" {
		return c.opts.Runtime.Logs(r.Context(), name)
	}
	logger, ok := c.opts.Runtime.(componentLogger)
	// Backends must not silently return another component's output.
	if !ok {
		return "", model.Err("UNSUPPORTED_COMPONENT", "Component logs unavailable")
	}
	return logger.ComponentLogs(r.Context(), name, component)
}

// validateEndpointReference resolves one declared application or sidecar URL.
func (c *Controller) validateEndpointReference(p model.Project, parts []string) error {
	// Sidecar references stay inside the parent group.
	if parts[0] == "sidecars" {
		m, ok := p.Sidecars[parts[1]]
		// Listenerless or absent members cannot provide a URL.
		if !ok || m.Port == 0 {
			return model.Err("INVALID_BINDING", "Sidecar endpoint unavailable")
		}
	}
	// Private services must explicitly admit the deploying application.
	if parts[0] == "applications" {
		target := c.state.Applications[parts[1]]
		// Require an active private provider and its consumer grant.
		if target == nil || target.Status != "running" || !allowsConsumer(target.Spec, p.Name) {
			return model.Err("PRIVATE_ACCESS", "Private service does not authorize this application")
		}
	}
	return nil
}

// validateProviderChange preserves addresses and grants used by running consumer groups.
func (c *Controller) validateProviderChange(next model.Project) error {
	old := c.state.Applications[next.Name]
	// A new application has no prior private endpoint contract.
	if old == nil {
		return nil
	}
	// Consumers retain their approved address until explicitly stopped or redeployed.
	for consumer := range c.activeProviderConsumers(next.Name) {
		// A running consumer prevents silently revoking its address or access grant.
		if !allowsConsumer(next, consumer) || next.Port != old.Spec.Port || model.DesiredReplicas(next) == 0 {
			return model.Err("PRIVATE_IN_USE", "Stop or redeploy consumers before changing their private endpoint or access grant")
		}
	}
	return nil
}

// usesProvider checks group-scoped bindings without granting access from arbitrary environment text.
func usesProvider(p model.Project, name string) bool {
	// A sidecar binding participates in the same parent application ownership boundary.
	for _, reference := range model.EnvironmentBindings(p) {
		// Endpoint references have a canonical exact spelling after validation.
		if reference == "applications."+name+".url" {
			return true
		}
	}
	return false
}

// validateProviderAction prevents lifecycle removal while approved consumers remain active.
func (c *Controller) validateProviderAction(app *Application, action string) error {
	// Restart preserves the endpoint identity and is an explicit maintenance action.
	if action != "stop" && action != "delete" {
		return nil
	}
	// Accepted consumer deployments also reserve their provider contract before they become active.
	if len(c.activeProviderConsumers(app.Name)) > 0 {
		return model.Err("PRIVATE_IN_USE", "Stop private consumers before stopping or deleting this application")
	}
	return nil
}

// activeProviderConsumers includes active processes and accepted work awaiting serialized execution.
func (c *Controller) activeProviderConsumers(provider string) map[string]bool {
	out := map[string]bool{}
	// Degraded groups still own processes and must not silently lose their provider grants.
	for _, consumer := range c.state.Applications {
		// Stopped/deleted groups release their active endpoint requirement.
		if consumer.Status == "running" || consumer.Status == "degraded" {
			// Only explicit typed bindings establish a dependency.
			if usesProvider(consumer.Spec, provider) {
				out[consumer.Name] = true
			}
		}
	}
	// Accepted operations reserve the same contract as an already running consumer.
	for _, op := range c.state.Operations {
		// Completed or failed operations have no pending consumer to protect.
		if op.Status == "queued" || op.Status == "running" {
			// Sidecar references belong to the parent operation identity.
			if usesProvider(op.Spec, provider) {
				out[op.Name] = true
			}
		}
	}
	return out
}
