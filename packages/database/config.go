// Package database defines provider-independent database package configuration.
// It performs offline validation and planning; it never resolves secrets or provisions resources.
package database

import (
	"errors"
	"regexp"
)

const (
	NewInstance = "new-instance"
	NewDatabase = "new-database"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
var name = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var secret = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Config describes an app-owned dependency without embedding credentials.
type Config struct {
	Version          int    `json:"version" yaml:"version"`
	App              string `json:"app" yaml:"app"`
	Name             string `json:"name" yaml:"name"`
	Engine           string `json:"engine" yaml:"engine"`
	Mode             string `json:"mode" yaml:"mode"`
	Database         string `json:"database,omitempty" yaml:"database,omitempty"`
	Collection       string `json:"collection,omitempty" yaml:"collection,omitempty"`
	ConnectionSecret string `json:"connectionSecret,omitempty" yaml:"connectionSecret,omitempty"`
	OutputSecret     string `json:"outputSecret" yaml:"outputSecret"`
}

// Capability distinguishes named databases from cache namespaces and search indexes.
type Capability struct {
	Engine      string `json:"engine"`
	NewDatabase bool   `json:"newDatabase"`
}

// Validate checks common fields and the provider's declared engine capability.
func (c Config) Validate(capabilities []Capability) error {
	// Only the initial schema is defined.
	if c.Version != 1 || !name.MatchString(c.App) || !name.MatchString(c.Name) {
		return errors.New("version must be 1; app and name must be lowercase portable names")
	}
	// Output references cannot contain connection strings or overwrite source credentials.
	if !SecretReference(c.OutputSecret) || c.OutputSecret == c.ConnectionSecret {
		return errors.New("outputSecret must be a distinct secret reference")
	}
	// Match capabilities rather than silently accepting arbitrary engine names.
	for _, capability := range capabilities {
		// Each provider owns its engine allowlist.
		if capability.Engine == c.Engine {
			return c.validateMode(capability)
		}
	}
	return errors.New("engine is not supported by this package")
}

// validateMode keeps new service configuration distinct from shared-server access.
func (c Config) validateMode(capability Capability) error {
	// Require one explicit lifecycle mode.
	switch c.Mode {
	// A new service obtains its connection from the provider.
	case NewInstance:
		// Existing administrative connections cannot accompany service creation.
		if c.ConnectionSecret != "" {
			return errors.New("new-instance cannot set connectionSecret")
		}
	// An existing service is accessed through a protected administrative connection.
	case NewDatabase:
		// Cache namespaces and search indexes are not independent named databases.
		if !capability.NewDatabase || !SecretReference(c.ConnectionSecret) {
			return errors.New("new-database requires a supported engine and connectionSecret")
		}
	// Unknown modes never fall back to paid service creation.
	default:
		return errors.New("mode must be new-instance or new-database")
	}
	return c.validateDatabase(capability)
}

// validateDatabase requires portable database identifiers and explicit MongoDB initialization.
func (c Config) validateDatabase(capability Capability) error {
	// Engines with named databases always select an explicit app database.
	if capability.NewDatabase && !identifier.MatchString(c.Database) {
		return errors.New("database must be a lowercase identifier of at most 48 characters")
	}
	// Reject settings which would otherwise be silently ignored for caches and search.
	if !capability.NewDatabase && c.Database != "" {
		return errors.New("this engine does not support named database configuration")
	}
	// MongoDB materializes an empty database through an explicit collection.
	if c.Engine == "mongodb" {
		// A collection makes provisioning observable before the application's first write.
		if !identifier.MatchString(c.Collection) {
			return errors.New("mongodb requires a portable initial collection name")
		}
		return nil
	}
	// Other engines do not consume collection settings.
	if c.Collection != "" {
		return errors.New("collection is only supported for mongodb")
	}
	return nil
}

// SecretReference validates a name, never a credential value.
func SecretReference(value string) bool { return secret.MatchString(value) }

// Label validates a bounded provider identifier without allowing whitespace or URLs.
func Label(value string) bool { return secret.MatchString(value) }
