package cli

import (
	"hostlier/internal/config"
	"hostlier/internal/model"
	"net/url"
	"strconv"
)

// scale derives an approved deployment from the current server desired specification.
func (s *session) scale() error {
	// Require an explicit application and at least one desired scaling value.
	if s.sub == "" || (s.flags["cpu"] == "" && s.flags["memory"] == "" && s.flags["replicas"] == "") {
		return model.Err("ARGUMENTS", "Usage: scale APP --cpu CORES --memory SIZE --replicas COUNT (supply at least one)")
	}
	return s.reviewPlan(planFlow{path: "/v1/plans", refresh: s.scalingPlanBody, routeSource: "server desired state", prompt: "Approve this exact scaling plan? [y/N] ", staleMessage: "Server state changed. Review and approve the updated scaling plan."})
}

// scaledProject applies only explicitly supplied values to a copy of the desired specification.
func scaledProject(spec model.Project, flags map[string]string) (model.Project, error) {
	// Manual capacity overrides revoke automatic replica ownership in the reviewed plan.
	spec.SleepAfter = ""
	spec.Autoscaling = nil
	limits := model.Limits{}
	// Preserve current per-replica limits without aliasing the source object.
	if spec.Resources != nil {
		limits = *spec.Resources
	}
	// Replace CPU only when supplied by the caller.
	if flags["cpu"] != "" {
		limits.CPU = flags["cpu"]
	}
	// Replace memory only when supplied by the caller.
	if flags["memory"] != "" {
		limits.Memory = flags["memory"]
	}
	spec.Resources = &limits
	// Parse replicas separately so explicit zero remains distinguishable from omission.
	if flags["replicas"] != "" {
		count, err := strconv.Atoi(flags["replicas"])
		// Reject noninteger desired counts before any plan request.
		if err != nil {
			return spec, model.Err("ARGUMENTS", "Replicas must be a nonnegative integer")
		}
		spec.Replicas = &count
	}
	return config.ValidateProject(spec)
}

// scalingPlanBody reapplies only explicit flags to freshly fetched desired state on each review.
func (s *session) scalingPlanBody() (map[string]any, error) {
	var app applicationSpec
	// Reading current desired state keeps omitted values current after a stale-plan retry.
	if err := s.request("GET", "/v1/apps/"+url.PathEscape(s.sub), nil, &app); err != nil {
		return nil, err
	}
	spec, err := scaledProject(app.Spec, s.flags)
	// Invalid patched limits or counts cannot produce a plan request.
	if err != nil {
		return nil, err
	}
	return map[string]any{"spec": spec, "action": "scale"}, nil
}
