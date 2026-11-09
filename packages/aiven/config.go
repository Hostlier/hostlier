// Package aiven validates optional Aiven database dependencies without linking into the controller.
package aiven

import (
	"errors"
	"hostlier/packages/database"
)

// Config selects either a paid service or a database through an existing connection.
type Config struct {
	database.Config `yaml:",inline"`
	Instance        *Instance `json:"instance,omitempty" yaml:"instance,omitempty"`
}

// Instance contains non-secret Aiven placement and an API token reference.
type Instance struct {
	TokenSecret string `json:"tokenSecret" yaml:"tokenSecret"`
	Project     string `json:"project" yaml:"project"`
	Service     string `json:"service" yaml:"service"`
	Plan        string `json:"plan" yaml:"plan"`
	Cloud       string `json:"cloud,omitempty" yaml:"cloud,omitempty"`
}

// Capabilities returns a fresh list so callers cannot mutate the package allowlist.
func Capabilities() []database.Capability {
	return []database.Capability{
		{Engine: "postgres", NewDatabase: true}, {Engine: "mysql", NewDatabase: true},
		{Engine: "clickhouse", NewDatabase: true}, {Engine: "valkey"}, {Engine: "opensearch"},
	}
}

// Validate rejects contradictory lifecycle options before any provider operation.
func (c Config) Validate() error {
	// Share lifecycle and credential-reference validation without coupling provider-specific placement schemas.
	if err := c.Config.ValidateProvider(Capabilities(), c.Instance != nil, "Aiven"); err != nil {
		return err
	}
	// Existing connections need no provider instance settings beyond the shared mode checks.
	if c.Mode == database.NewDatabase {
		return nil
	}
	return c.validateInstance()
}

// validateInstance checks syntax only; regional availability and pricing require provider preflight.
func (c Config) validateInstance() error {
	i := c.Instance
	// Preserve input API credentials when publishing the eventual application connection.
	if err := database.ValidateTokenReference(i.TokenSecret, c.OutputSecret); err != nil {
		return err
	}
	// Require deliberate project, service identity and paid plan choices.
	if !database.Label(i.Project) || !database.Label(i.Service) || !database.Label(i.Plan) {
		return errors.New("Aiven instance requires project, service and plan identifiers")
	}
	// Some Aiven tiers choose their own cloud; an explicit cloud must be a bounded identifier.
	if i.Cloud != "" && !database.Label(i.Cloud) {
		return errors.New("invalid Aiven cloud identifier")
	}
	return nil
}

// Plan produces an offline workflow preview without calling Aiven or reading secrets.
func (c Config) Plan() (database.Plan, error) {
	// Invalid combinations never produce plans.
	if err := c.Validate(); err != nil {
		return database.Plan{}, err
	}
	steps := []database.Step{}
	// Provision once and reconcile durable ownership on subsequent app deployments.
	if c.Mode == database.NewInstance {
		steps = append(steps,
			database.Step{Action: "preflight-aiven", Detail: "Resolve token; verify project, engine, plan, cloud availability, cost and reachable TLS endpoint"},
			database.Step{Action: "ensure-instance", Detail: "Create the configured service only if absent and unclaimed; persist provider ID before continuing; reuse the owned instance on redeploy"},
			database.Step{Action: "wait-ready", Detail: "Wait for the service and retrieve its protected connection"})
	}
	steps = append(steps, database.DatabaseSteps(c.Config)...)
	return database.Preview("aiven", c.Config, c, steps)
}
