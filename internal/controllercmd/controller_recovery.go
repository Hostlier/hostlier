package controllercmd

import (
	"fmt"
	"hostlier/internal/store"
	"os"
)

// controllerRecovery executes offline backups or restores under exclusive state ownership.
func (s *session) controllerRecovery() error {
	dir := defaultValue(s.flags["state-dir"], "/var/lib/hostlier")
	// Require explicit local paths before reading protected controller material.
	switch s.sub {
	// Select the explicitly requested recovery operation.
	case "backup":
		// Validate the precondition before accepting protected state.
		if s.flags["output"] == "" {
			return fmt.Errorf("controller backup requires --output FILE")
		}
		// Propagate failures before publishing recovery state.
		if err := store.Export(dir, s.flags["output"], s.recoveryConfig()); err != nil {
			return err
		}
	// Select the explicitly requested recovery operation.
	case "restore":
		// Validate the precondition before accepting protected state.
		if s.flags["source-stopped"] != "true" {
			return fmt.Errorf("restore requires --source-stopped after stopping the original controller")
		}
		// Validate the precondition before accepting protected state.
		if s.flags["input"] == "" {
			return fmt.Errorf("controller restore requires --input FILE and a new --state-dir DIR")
		}
		// Propagate failures before publishing recovery state.
		if err := store.Restore(s.flags["input"], dir); err != nil {
			return err
		}
	// Select the explicitly requested recovery operation.
	default:
		return fmt.Errorf("unsupported controller recovery command")
	}
	_, err := fmt.Fprintln(s.out, "Controller metadata recovery complete. Keep the original controller stopped; database contents and application volumes require separate backups.")
	return err
}

// recoveryConfig includes the installed configuration when no custom path was supplied.
func (s *session) recoveryConfig() string {
	// A custom server configuration must be explicitly included in its recovery bundle.
	if path := s.flags["config"]; path != "" {
		return path
	}
	const installed = "/etc/hostlier/config.yaml"
	// Development stores may have no installed server configuration.
	if _, err := os.Stat(installed); err == nil {
		return installed
	}
	return ""
}
