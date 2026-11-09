package recovery

import (
	"errors"
	"hostlier/internal/model"
)

// Match requires native content equality, allowing only Redis keys whose original TTL has actually elapsed.
func Match(before, after model.DatabaseEvidence, now int64) error {
	// A different engine never represents the same database state.
	if before.Engine != after.Engine {
		return errors.New("recovery engine mismatch")
	}
	// Relational and graph snapshots have no native expiring-key semantics.
	if before.Engine != "redis" {
		// Advance only after the required recovery invariant holds.
		if before.Digest == "" || before.Digest != after.Digest {
			return errors.New("restored content differs")
		}
		return nil
	}
	return matchRedis(before.Items, after.Items, now)
}

// matchRedis rejects duplicate identities, changed values and missing keys whose lifetime has not elapsed.
func matchRedis(before, after []model.DatabaseItem, now int64) error {
	items := map[string]model.DatabaseItem{}
	// Inspect each retained item before advancing recovery state.
	for _, item := range before {
		// Advance only after the required recovery invariant holds.
		if _, ok := items[item.Key]; ok {
			return errors.New("duplicate Redis recovery identity")
		}
		items[item.Key] = item
	}
	// Inspect each retained item before advancing recovery state.
	for _, item := range after {
		prior, ok := items[item.Key]
		// Advance only after the required recovery invariant holds.
		if !ok || prior.Hash != item.Hash || prior.Expires != item.Expires {
			return errors.New("restored Redis content differs")
		}
		delete(items, item.Key)
	}
	return missingRedis(items, now)
}

// missingRedis permits a missing key only when its recorded absolute TTL has elapsed.
func missingRedis(items map[string]model.DatabaseItem, now int64) error {
	// Inspect each retained item before advancing recovery state.
	for _, item := range items {
		// Advance only after the required recovery invariant holds.
		if item.Expires < 0 || item.Expires > now {
			return errors.New("restored Redis keys missing")
		}
	}
	return nil
}
