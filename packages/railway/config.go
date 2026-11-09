// Package railway validates optional Railway database dependencies independently of the controller.
package railway

import (
	"errors"
	"hostlier/packages/database"
)

// Config declares a new Railway service or a database on an existing reachable server.
type Config struct {
	database.Config `yaml:",inline"`
	Instance        *Instance `json:"instance,omitempty" yaml:"instance,omitempty"`
}

// Instance makes project, environment, template and external connectivity explicit.
type Instance struct {
	TokenSecret  string `json:"tokenSecret" yaml:"tokenSecret"`
	TokenType    string `json:"tokenType" yaml:"tokenType"`
	Project      string `json:"project" yaml:"project"`
	Environment  string `json:"environment" yaml:"environment"`
	Service      string `json:"service" yaml:"service"`
	Template     string `json:"template" yaml:"template"`
	PublicAccess bool   `json:"publicAccess" yaml:"publicAccess"`
}

// Capabilities intentionally covers official database engines, not arbitrary marketplace images.
func Capabilities() []database.Capability {
	return []database.Capability{
		{Engine: "postgres", NewDatabase: true}, {Engine: "mysql", NewDatabase: true},
		{Engine: "redis"}, {Engine: "mongodb", NewDatabase: true},
	}
}

// Validate checks mode semantics before interpreting provider-specific placement.
func (c Config) Validate() error {
	// Share lifecycle and credential-reference validation without coupling provider-specific placement schemas.
	if err := c.Config.ValidateProvider(Capabilities(), c.Instance != nil, "Railway"); err != nil {
		return err
	}
	// Existing connections need no provider instance settings beyond the shared mode checks.
	if c.Mode == database.NewDatabase {
		return nil
	}
	return c.validateInstance()
}

// validateInstance checks credential references and explicit provider placement.
func (c Config) validateInstance() error {
	i := c.Instance
	// Preserve input API credentials when publishing the eventual application connection.
	if err := database.ValidateTokenReference(i.TokenSecret, c.OutputSecret); err != nil {
		return err
	}
	// Project-scoped credentials use a different HTTP header from account credentials.
	if i.TokenType != "account" && i.TokenType != "workspace" && i.TokenType != "project" {
		return errors.New("Railway tokenType must be account, workspace or project")
	}
	return c.validatePlacement()
}

// validatePlacement requires a reviewed template and a reachable endpoint for external Hostlier servers.
func (c Config) validatePlacement() error {
	i := c.Instance
	// Provider IDs are opaque bounded identifiers, not URLs or credential values.
	if !database.Label(i.Project) || !database.Label(i.Environment) || !database.Label(i.Service) || !database.Label(i.Template) {
		return errors.New("Railway instance requires project, environment, service and template identifiers")
	}
	// A private Railway hostname is unreachable from an ordinary external Hostlier server.
	if !i.PublicAccess {
		return errors.New("Railway new-instance requires explicit publicAccess for external Hostlier connectivity")
	}
	return nil
}

// Plan describes provider and engine work without sending GraphQL requests or exposing credentials.
func (c Config) Plan() (database.Plan, error) {
	// Validation is mandatory for every public planning call.
	if err := c.Validate(); err != nil {
		return database.Plan{}, err
	}
	steps := []database.Step{}
	// Reconciliation uses persistent provider identity, not one new service per deployment.
	if c.Mode == database.NewInstance {
		steps = append(steps,
			database.Step{Action: "preflight-railway", Detail: "Verify token scope, project, environment and template engine; resolve template version, persistent volume, capacity, TLS support and cost for approval"},
			database.Step{Action: "ensure-instance", Detail: "Create the service and persistent volume once; persist provider IDs and refuse adoption of unowned services"},
			database.Step{Action: "ensure-public-access", Detail: "Configure external connectivity and review exposure and egress charges; require verified TLS for the database connection"},
			database.Step{Action: "wait-ready", Detail: "Wait for service readiness and resolve the external connection, never a private Railway hostname"})
	}
	steps = append(steps, database.DatabaseSteps(c.Config)...)
	return database.Preview("railway", c.Config, c, steps)
}
