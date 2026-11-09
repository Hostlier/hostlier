package controller

import "time"

// boundedPlanningTimeout permits administrator tuning without unbounded provider work.
func boundedPlanningTimeout(value time.Duration) time.Duration {
	// Ordinary IaC preparation has a ten-minute budget independent of HTTP requests.
	if value <= 0 {
		return 10 * time.Minute
	}
	// Even administrator configuration cannot disable the hard operation bound.
	if value > time.Hour {
		return time.Hour
	}
	return value
}
