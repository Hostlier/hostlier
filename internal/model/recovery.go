package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RecoveryOrigin retains the exact operational path used by existing runtime ownership hashes.
type RecoveryOrigin struct {
	StateDirectory string `json:"stateDirectory"`
}

// ValidateRecoveryLocation fences restored staging metadata from activating under a different resource namespace.
func ValidateRecoveryLocation(dir string) error {
	path := filepath.Join(dir, "recovery-origin.json")
	info, err := os.Lstat(path)
	// Ordinary controllers without a recovery marker retain their existing behavior.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// Marker inspection must fail closed on permission or filesystem errors.
	if err != nil {
		return err
	}
	// Ownership metadata cannot redirect to a mutable external file.
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("unsafe controller recovery origin marker")
	}
	data, err := os.ReadFile(path)
	// Do not activate runtime adapters without a readable original ownership path.
	if err != nil {
		return err
	}
	var origin RecoveryOrigin
	// An invalid or missing origin cannot authorize taking ownership of runtime resources.
	if json.Unmarshal(data, &origin) != nil || !filepath.IsAbs(origin.StateDirectory) {
		return fmt.Errorf("invalid controller recovery origin marker")
	}
	// Runtime and database resource ownership hash the operational state path itself.
	if dir != origin.StateDirectory {
		return fmt.Errorf("restored metadata is staged at %s; activate only at original state directory %s after stopping the source controller", dir, origin.StateDirectory)
	}
	return nil
}
