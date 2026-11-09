package config

import (
	"encoding/json"
	"hostlier/internal/model"
	"strings"
)

// validateContainerSecurity admits an explicit confined profile without privileged or unconfined escape hatches.
func validateContainerSecurity(p model.Project) (model.Project, error) {
	s := p.Security
	// Existing projects retain their original image/user semantics until explicitly opted in.
	if s == nil {
		return p, nil
	}
	// Independent boundaries remain small enough to review and test in isolation.
	for _, validate := range []func(model.Project) error{validateSecurityIdentity, validateSecurityHardening, validateSecurityBudgets, validateSeccompReference} {
		// Any unsupported setting rejects the complete deployment before mutation.
		if err := validate(p); err != nil {
			return p, err
		}
	}
	return p, validateScratchMounts(p)
}

// ValidateContainerSecurity enforces the same boundary for direct runtime callers.
func ValidateContainerSecurity(p model.Project) error {
	_, err := validateContainerSecurity(p)
	return err
}

// validateSeccompReference requires a prepared public config file with a default-deny syscall policy.
func validateSeccompReference(p model.Project) error {
	// Docker's default seccomp remains active when no custom profile is selected.
	if p.Security.SeccompConfig == "" {
		return nil
	}
	file, ok := p.Configs[p.Security.SeccompConfig]
	// Secret or unresolved inputs cannot hide syscall policy from approval review.
	if !ok || file.Secret != "" || file.File != "" {
		return invalid("security.seccompConfig must reference a prepared nonsecret managed config")
	}
	return validateSeccompContent(file.Content)
}

// validateSecurityIdentity prevents root and overflow/sentinel user identities.
func validateSecurityIdentity(p model.Project) error {
	s := p.Security
	// Both IDs must select real unprivileged identities under the version-two contract.
	if p.Version != 2 || s.RunAsUser == 0 || s.RunAsGroup == 0 || s.RunAsUser == ^uint32(0) || s.RunAsGroup == ^uint32(0) {
		return invalid("security requires version 2 and nonroot numeric runAsUser/runAsGroup")
	}
	return nil
}

// validateSecurityHardening rejects custom policy without a confined outer process.
func validateSecurityHardening(p model.Project) error {
	s := p.Security
	// No elevated root filesystem, capabilities or privilege transitions are exposed by this contract.
	if !s.ReadOnlyRootFilesystem || !s.NoNewPrivileges || !s.NoSwap || len(s.DropCapabilities) != 1 || s.DropCapabilities[0] != "ALL" {
		return invalid("security requires readOnlyRootFilesystem, noNewPrivileges, noSwap and dropCapabilities: [ALL]")
	}
	return nil
}

// validateSecurityBudgets bounds process growth and permits only explicit host-managed AppArmor names.
func validateSecurityBudgets(p model.Project) error {
	s := p.Security
	// A no-swap contract needs an explicit memory allocation to pin its total limit.
	if p.Resources == nil || p.Resources.Memory == "" {
		return invalid("security requires an explicit resources.memory limit")
	}
	// Thread/process budgets remain finite independently of request concurrency.
	if s.PidsLimit < 16 || s.PidsLimit > 4096 {
		return invalid("security.pidsLimit must be between 16 and 4096")
	}
	// A custom AppArmor name must select a confined policy installed by the server administrator.
	if s.AppArmorProfile != "" && (!strings.HasPrefix(s.AppArmorProfile, "hostlier-") || !nameRE.MatchString(s.AppArmorProfile)) {
		return invalid("Use an installed hostlier- AppArmor profile; unconfined is unsupported")
	}
	return nil
}

// reviewedSeccompProfile keeps default-deny semantics visible while Docker validates full architecture/argument rules.
type reviewedSeccompProfile struct {
	DefaultAction string                `json:"defaultAction"`
	Syscalls      []reviewedSyscallRule `json:"syscalls"`
}

// reviewedSyscallRule exposes only literal syscall selection and ordinary allow/deny actions.
type reviewedSyscallRule struct {
	Names  []string `json:"names"`
	Action string   `json:"action"`
}

// validateSeccompContent bounds and inspects the actual immutable profile bytes.
func validateSeccompContent(content string) error {
	var profile reviewedSeccompProfile
	// Controller and direct runtime callers share the same finite JSON byte boundary.
	if len(content) > 65536 || json.Unmarshal([]byte(content), &profile) != nil {
		return invalid("Invalid or oversized seccomp JSON")
	}
	// An allow-by-default or empty policy cannot serve as worker confinement.
	if profile.DefaultAction != "SCMP_ACT_ERRNO" || len(profile.Syscalls) == 0 || len(profile.Syscalls) > 1024 {
		return invalid("seccomp requires bounded JSON with defaultAction SCMP_ACT_ERRNO")
	}
	// Each rule has separately bounded action and syscall identifiers.
	for _, rule := range profile.Syscalls {
		// Unknown actions or wildcard selectors invalidate the complete profile.
		if err := validateSyscallRule(rule); err != nil {
			return err
		}
	}
	return nil
}

// validateSyscallRule rejects wildcard names and action types outside the reviewed worker contract.
func validateSyscallRule(rule reviewedSyscallRule) error {
	// User-notification and implicit wildcard policies are not accepted.
	if rule.Action != "SCMP_ACT_ALLOW" && rule.Action != "SCMP_ACT_ERRNO" || len(rule.Names) == 0 || len(rule.Names) > 512 {
		return invalid("Invalid seccomp syscall action or names")
	}
	// Identifiers are literal Linux syscall names rather than paths or expressions.
	for _, name := range rule.Names {
		// The environment identifier grammar admits ordinary syscall names with no interpolation.
		if len(name) > 64 || !envRE.MatchString(name) {
			return invalid("Invalid seccomp syscall name")
		}
	}
	return nil
}

// validateScratchMounts keeps writable temporary data bounded and disjoint from retained or managed files.
func validateScratchMounts(p model.Project) error {
	// An opted-in profile has only a small explicit scratch mount inventory.
	if len(p.Security.Tmpfs) > 4 {
		return invalid("At most four bounded tmpfs mounts are supported")
	}
	seen := map[string]bool{}
	// Each mount has one normalized target and fixed nosuid/nodev runtime semantics.
	for mount, scratch := range p.Security.Tmpfs {
		size, err := ParseMemory(scratch.Size)
		// Executable scratch is allowed for worker binaries, but device/suid bits are never configurable.
		if err != nil || size > 1<<30 || !validConfigMount(mount) || scratch.Mode&^01777 != 0 {
			return invalid("tmpfs requires an absolute non-kernel path, read/write permission mode and size up to 1Gi")
		}
		// Scratch cannot shadow persistent or sibling temporary storage.
		if err := validateConfigMounts(p, mount, seen); err != nil {
			return err
		}
		// Managed configuration remains independent from writable scratch directories.
		for _, file := range p.Configs {
			// An overlapping temporary tree would hide the approved file mount.
			if overlappingMount(mount, file.Mount) {
				return invalid("tmpfs overlaps a managed config")
			}
		}
		seen[mount] = true
	}
	return nil
}
