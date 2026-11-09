package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Step is declarative workflow intent, not executable SQL or an API request.
type Step struct {
	Action string `json:"action"`
	Detail string `json:"detail"`
}

// Plan is an offline preview. It is deliberately not an apply token for the controller.
type Plan struct {
	Version          int             `json:"version"`
	Package          string          `json:"package"`
	Phase            string          `json:"phase"`
	Executable       bool            `json:"executable"`
	ApprovalRequired bool            `json:"approvalRequired"`
	ResourceKey      string          `json:"resourceKey"`
	Fingerprint      string          `json:"fingerprint"`
	Retention        string          `json:"retention"`
	Configuration    json.RawMessage `json:"configuration"`
	Steps            []Step          `json:"steps"`
}

// Preview records the complete validated configuration and a stable desired resource identity.
func Preview(provider string, c Config, configuration any, steps []Step) (Plan, error) {
	snapshot, err := json.Marshal(configuration)
	// Snapshot pointer-backed provider settings so later edits cannot change a reviewed preview.
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		Version: 1, Package: provider, Phase: "before-app-deploy", ApprovalRequired: true,
		ResourceKey: provider + "/" + c.App + "/" + c.Name,
		Retention:   "retain-on-app-delete", Configuration: snapshot, Steps: append([]Step(nil), steps...),
	}
	encoded, err := json.Marshal(plan)
	// Unsupported configuration representations cannot yield an approvable fingerprint.
	if err != nil {
		return Plan{}, err
	}
	sum := sha256.Sum256(encoded)
	plan.Fingerprint = hex.EncodeToString(sum[:])
	return plan, nil
}

// DatabaseSteps describes credential isolation and secret publication for each engine.
func DatabaseSteps(c Config) []Step {
	steps := []Step{{Action: "verify-connection", Detail: "Verify TLS, engine, reachability and administrative privileges"}}
	// Reused named databases require ownership checks even when their parent instance was originally app-created.
	if c.Database != "" {
		steps = append(steps,
			Step{"check-ownership", "Reject an existing database unless durable state identifies it as this dependency's resource"},
			Step{"ensure-database", "Create or reuse only the owned named database; preserve the parent instance"})
	}
	// MongoDB requires an explicit collection to materialize the database.
	if c.Collection != "" {
		steps = append(steps, Step{"ensure-collection", "Create the configured initial collection"})
	}
	steps = append(steps, Step{"ensure-application-credentials", "Create or verify engine-supported application-scoped credentials; reject administrative credentials or unsupported isolation"})
	return append(steps, Step{"publish-secret", "Store the application connection in outputSecret; never publish the administrative connection"})
}
