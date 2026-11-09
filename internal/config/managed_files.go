package config

import (
	"hostlier/internal/model"
	"path"
	"strings"
	"unicode/utf8"
)

// validateManagedConfigs bounds reviewed contents and enforces unique file mount ownership.
func validateManagedConfigs(p model.Project) (model.Project, error) {
	// File lifecycle and storage quotas are an explicit version-two contract.
	if len(p.Configs) > 32 || len(p.Configs) > 0 && p.Version != 2 {
		return p, invalid("Managed configs require version 2 and at most 32 files")
	}
	mounts := map[string]bool{}
	// Every file is checked against its siblings and persistent volume destinations.
	for name, file := range p.Configs {
		value, err := normalizeConfigFile(name, file)
		// A malformed file aborts the whole revision.
		if err != nil {
			return p, err
		}
		// Segment-aware overlaps cannot hide or shadow another source.
		if err = validateConfigMounts(p, value.Mount, mounts); err != nil {
			return p, err
		}
		mounts[value.Mount] = true
		p.Configs[name] = value
	}
	// Aggregate limits also cover many individually valid files.
	if managedConfigBytes(p) > 262144 {
		return p, invalid("Managed configs exceed 256 KiB")
	}
	return p, nil
}

// ValidateManagedConfigs applies the same immutable boundary for direct runtime callers.
func ValidateManagedConfigs(p model.Project) (model.Project, error) { return validateManagedConfigs(p) }

// normalizeConfigFile validates a single source and selects a read-only mode.
func normalizeConfigFile(name string, file model.ConfigFile) (model.ConfigFile, error) {
	// Controller planning never reads a CLI-local source path.
	if !nameRE.MatchString(name) || file.File != "" {
		return file, invalid("Prepare managed config file sources before planning")
	}
	// Files must contain bounded UTF-8 text and name one canonical mount target.
	if !validConfigMount(file.Mount) || !utf8.ValidString(file.Content) || len(file.Content) > 65536 {
		return file, invalid("Invalid managed config mount or content")
	}
	// Encrypted references cannot contain an alternate plaintext source.
	if file.Secret != "" && (file.Content != "" || !secretRE.MatchString(file.Secret)) {
		return file, invalid("Invalid managed config secret reference")
	}
	// Bind mounts are read-only; their parent host directory remains owner-only.
	if file.Mode == 0 {
		file.Mode = 0444
	}
	return file, validateConfigMode(file.Mode)
}

// validateConfigMode excludes executable and writable file permissions.
func validateConfigMode(mode uint32) error {
	// Read bits alone are meaningful for immutable application configuration.
	if mode&^0444 != 0 || mode&0444 == 0 {
		return invalid("Managed configs require read-only file modes")
	}
	return nil
}

// validateConfigMounts prevents one file, directory or volume from shadowing another.
func validateConfigMounts(p model.Project, mount string, seen map[string]bool) error {
	// Files cannot be mounted above or below another file's path.
	for other := range seen {
		// A file cannot hide a sibling file or one of its path ancestors.
		if overlappingMount(mount, other) {
			return invalid("Managed config mounts overlap")
		}
	}
	// Persistent application data retains its own independent write ownership.
	for _, volume := range p.Volumes {
		// Configuration cannot replace application-owned persistent data.
		if overlappingMount(mount, volume.Mount) {
			return invalid("Managed config overlaps a persistent volume")
		}
	}
	return nil
}

// validConfigMount rejects process/kernel state and relative or aliased paths.
func validConfigMount(value string) bool {
	// Targets name an absolute file and cannot carry NUL or directive delimiters.
	if value == "/" || !strings.HasPrefix(value, "/") || path.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	// Application files cannot shadow kernel or device filesystems.
	for _, root := range []string{"/proc", "/sys", "/dev"} {
		// Kernel and device mounts remain outside application configuration.
		if value == root || strings.HasPrefix(value, root+"/") {
			return false
		}
	}
	return true
}

// overlappingMount compares path segments instead of unrelated textual prefixes.
func overlappingMount(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// managedConfigBytes bounds the complete process group's immutable plaintext configuration.
func managedConfigBytes(p model.Project) int {
	total := 0
	// File snapshots are counted before JSON escaping and controller approval.
	for _, file := range p.Configs {
		total += len(file.Content)
	}
	// Member configuration shares the same request and deployment budget.
	for _, member := range p.Sidecars {
		total += managedConfigBytes(member)
	}
	return total
}
