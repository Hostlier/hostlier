package config

import (
	"time"

	"hostlier/internal/model"
)

// validateIdlePolicy binds public HTTP demand to a single approved process.
func validateIdlePolicy(p model.Project) (model.Project, error) {
	// Ordinary deployments keep manual capacity ownership.
	if p.SleepAfter == "" {
		return p, nil
	}
	after, err := time.ParseDuration(p.SleepAfter)
	// Cap clock durations and avoid subsecond container churn.
	if err != nil || after < time.Second || after > 30*24*time.Hour {
		return p, invalid("sleepAfter must be a duration between 1s and 720h")
	}

	// The gateway maps exactly one public hostname to a private service.
	if !idleRootRoute(p.Route) || !idleHTTP(p) || !idleWorkload(p) {
		return p, invalid("sleepAfter requires a single stateless HTTP service at a root route with one replica and no CPU autoscaler")
	}
	p.SleepAfter = after.String()
	return p, nil
}

// idleHTTP accepts a single public listener whose backend speaks HTTP/1.
func idleHTTP(p model.Project) bool {
	return model.Public(p) && len(p.Endpoints) <= 1 && (p.Endpoint == nil || p.Endpoint.Protocol == "" || p.Endpoint.Protocol == "http")
}

// idleWorkload excludes other capacity owners, background workers and storage writers.
func idleWorkload(p model.Project) bool {
	return p.Workload != "worker" && len(p.Sidecars) == 0 && !model.HasVolumes(p) && model.DesiredReplicas(p) == 1 && p.Autoscaling == nil
}

// Omitted routes acquire the ordinary app hostname during planning.
func idleRootRoute(value string) bool {
	// An explicit route must reserve an entire hostname for this service.
	if value == "" {
		return true
	}
	route, err := ParseRoute(value, nil, "")
	return err == nil && route.Path == "/"
}
