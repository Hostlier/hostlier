package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// managedModesFile records approved read-only modes inside the checksummed recovery bundle.
const managedModesFile = "managed-config-modes.json"

// collectManagedConfigModes preserves container-readable permissions beneath private host directories.
func collectManagedConfigModes(dir string, files map[string][]byte) error {
	modes := map[string]uint32{}
	// Only already-collected regular managed files participate in this metadata index.
	for name := range files {
		// Other controller files keep their existing owner-only recovery semantics.
		if !strings.HasPrefix(name, "config-files/") {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name)))
		// A racing or writable source cannot become an approved read-only file during recovery.
		if err != nil || !info.Mode().IsRegular() || !readOnlyConfigMode(uint32(info.Mode().Perm())) {
			return errors.New("invalid managed config backup permissions")
		}
		modes[name] = uint32(info.Mode().Perm())
	}
	data, err := json.Marshal(modes)
	// Encoding failure cannot publish incomplete mode metadata.
	if err != nil {
		return err
	}
	files[managedModesFile] = data
	return nil
}

// restoredManagedConfigModes validates mode metadata before recreating any recovery files.
func restoredManagedConfigModes(files map[string][]byte) (map[string]uint32, error) {
	modes := map[string]uint32{}
	// Older bundles without managed files retain their existing restore format.
	if data, ok := files[managedModesFile]; ok {
		// Invalid indexes cannot silently restore inaccessible or executable files.
		if err := json.Unmarshal(data, &modes); err != nil {
			return nil, err
		}
	}
	// Metadata can change permissions only for an included managed-file payload.
	for name, mode := range modes {
		_, exists := files[name]
		// Controller executables, keys and traversal paths cannot receive file permissions from this index.
		if !exists || !strings.HasPrefix(name, "config-files/") || !readOnlyConfigMode(mode) {
			return nil, errors.New("invalid managed config recovery mode")
		}
	}
	// Every managed file needs its exact approved read-only mode.
	for name := range files {
		// Missing entries cannot fall back to the controller's unreadable-in-container mode 0600.
		if strings.HasPrefix(name, "config-files/") && modes[name] == 0 {
			return nil, errors.New("managed config recovery mode missing")
		}
	}
	return modes, nil
}

// readOnlyConfigMode matches the immutable file schema without permitting execution or write bits.
func readOnlyConfigMode(mode uint32) bool { return mode&^0444 == 0 && mode&0444 != 0 }
