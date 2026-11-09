package controller

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// validateDomains ensures configured domains continue to authorize every reserved deployment.
func (c *Controller) validateDomains(domains []string) error {
	// Include every retained application when enforcing the shared state invariant.
	for _, app := range c.state.Applications {
		// Keep the routes of active applications authorized after domain changes.
		if app.Status != "deleted" && model.Public(app.Spec) {
			// Reject this path: Configured domains no longer authorize an active application.
			if err := validateProjectDomains(app.Spec, domains, c.state.Hostname); err != nil {
				return model.Err("DOMAIN_IN_USE", "Configured domains no longer authorize an active application")
			}
		}
	}
	// Inspect accepted operations as well as active state before allowing conflicting work.
	for _, op := range c.state.Operations {
		// Keep domains reserved by accepted or recoverable deployments.
		if reservedPublicOperation(op) {
			// Reject this path: Configured domains no longer authorize an accepted deployment.
			if err := validateProjectDomains(op.Spec, domains, c.state.Hostname); err != nil {
				return model.Err("DOMAIN_IN_USE", "Configured domains no longer authorize an accepted deployment")
			}
		}
	}
	return nil
}

// setDomains validates and persists domain changes before updating authoritative in-memory state.
func (c *Controller) setDomains(domains []string) error {
	// Keep every active or accepted route authorized by the configured domains.
	if err := c.validateDomains(domains); err != nil {
		return err
	}
	validated, err := config.ValidateServer(model.ServerConfig{Version: 1, Domains: domains})
	// Reject an invalid server domain allowlist before changing authoritative state.
	if err != nil {
		return err
	}
	domains = validated.Domains
	// Preserve pending manual configuration edits instead of overwriting them.
	if err := c.checkDomainConfig(); err != nil {
		return err
	}
	// Keep every active or accepted route authorized by the configured domains.
	if err := c.validateDomains(domains); err != nil {
		return err
	}
	// Update the on-disk configuration when this controller has an authoritative config file.
	if c.opts.ConfigPath != "" {
		// Persist the domain allowlist before changing the authoritative in-memory copy.
		if err := c.writeDomainConfig(domains); err != nil {
			return err
		}
	}
	c.state.Domains = append([]string{}, domains...)
	c.state.Generation++
	return nil
}

// checkDomainConfig prevents API writes from overwriting unreviewed manual configuration changes.
func (c *Controller) checkDomainConfig() error {
	// Check authoritative on-disk configuration before applying API edits.
	if c.opts.ConfigPath != "" {
		onDisk, readErr := config.ReadServer(c.opts.ConfigPath)
		// Reject this path: Server configuration changed on disk; restart to validate and load it before editing domains.
		if readErr != nil || config.Fingerprint(onDisk.Domains) != config.Fingerprint(c.state.Domains) {
			return model.Err("CONFIG_CHANGED", "Server configuration changed on disk; restart to validate and load it before editing domains")
		}
	}

	return nil
}

// writeDomainConfig atomically replaces configuration and syncs its directory for durability.
func (c *Controller) writeDomainConfig(domains []string) error {
	data, err := yaml.Marshal(model.ServerConfig{Version: 1, Domains: domains})
	// Require valid serialized configuration before writing a replacement artifact.
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(c.opts.ConfigPath), ".hostlier-config-*")
	// Reject this path: Cannot update server configuration.
	if err != nil {
		return model.Err("CONFIG_WRITE_FAILED", "Cannot update server configuration")
	}
	name := file.Name()
	defer os.Remove(name)
	// Finish and sync the temporary configuration before replacing the active file.
	if err := writeConfigFile(file, data); err != nil {
		return err
	}
	// Reject this path: Cannot activate server configuration.
	if err = os.Rename(name, c.opts.ConfigPath); err != nil {
		return model.Err("CONFIG_WRITE_FAILED", "Cannot activate server configuration")
	}
	directory, err := os.Open(filepath.Dir(c.opts.ConfigPath))
	// Sync the configuration directory after opening it successfully.
	if err == nil {
		err = directory.Sync()
		directory.Close()
	}
	// Reject this path: Cannot durably commit server configuration.
	if err != nil {
		return model.Err("CONFIG_WRITE_FAILED", "Cannot durably commit server configuration")
	}

	return nil
}

// writeConfigFile writes and syncs the replacement configuration before activation.
func writeConfigFile(file *os.File, data []byte) error {
	var err error
	// Write configuration bytes only after applying their intended permissions.
	if err = file.Chmod(0644); err == nil {
		_, err = file.Write(data)
	}
	// Sync configuration bytes only after writing succeeds.
	if err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	// Preserve an earlier write or sync failure over a later close result.
	if err == nil {
		err = closed
	}
	// Reject this path: Cannot commit server configuration.
	if err != nil {
		return model.Err("CONFIG_WRITE_FAILED", "Cannot commit server configuration")
	}

	return nil
}

// reservedPublicOperation keeps domains only for public accepted or recoverable deployments.
func reservedPublicOperation(op *Operation) bool {
	return op.Kind == "deploy" && model.Public(op.Spec) && (op.Status == "queued" || op.Status == "running" || op.Status == "interrupted")
}

// validateProjectDomains preserves domain authority for every named public route.
func validateProjectDomains(p model.Project, domains []string, hostname string) error {
	// Secondary gRPC or HTTP listeners cannot outlive their approved domain.
	for _, endpoint := range model.EndpointProjects(p) {
		// Every endpoint must retain an authorized domain after this change.
		if _, err := config.ParseRoute(endpoint.Route, domains, hostname); err != nil {
			return err
		}
	}
	return nil
}
