package cli

import (
	"hostlier/internal/githubpackages"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
)

// availablePackageCommand supports explicit discovery and an unambiguous repository-list alias.
func (s *session) availablePackageCommand() bool {
	return s.sub == "available" || (s.sub == "list" && (s.flags["repository"] != "" || s.flags["github"] != "" || s.flags["version"] != ""))
}

// availablePackages discovers published packages using only CLI-local GitHub credentials.
func (s *session) availablePackages() error {
	// Catalog requests cannot silently mix filesystem installation or multiple target selections.
	if err := s.validateAvailablePackages(); err != nil {
		return err
	}
	var target *packageexec.Platform
	// An explicit controller selects a compatible subset using read-only platform discovery.
	if s.flags["server"] != "" || s.flags["socket"] != "" {
		platform, err := s.availablePackagePlatform()
		// Connection failures must not fall back to an unrelated machine's architecture.
		if err != nil {
			return err
		}
		target = &platform
	}
	catalog, err := s.packageCatalog()
	// Missing releases and private-access failures are visible errors, not empty installed inventories.
	if err != nil {
		return err
	}
	// Unfiltered discovery displays every published supported platform.
	if target != nil {
		catalog = compatiblePackages(catalog, *target)
	}
	return s.print(catalog)
}

// validateAvailablePackages rejects ambiguous source or destination flags before network access.
func (s *session) validateAvailablePackages() error {
	// Catalog discovery accepts no package name, file, state directory or approval bypass.
	if len(s.parsed.args) != 2 || s.flags["file"] != "" || s.flags["state-dir"] != "" || s.flags["yes"] != "" {
		return model.Err("ARGUMENTS", packageUsage)
	}
	// A server and socket cannot both define the filter platform.
	if s.flags["server"] != "" && s.flags["socket"] != "" {
		return model.Err("ARGUMENTS", "Select one --server or --socket")
	}
	return s.validatePackageRepository(false)
}

// availablePackagePlatform queries only the selected controller's execution target.
func (s *session) availablePackagePlatform() (packageexec.Platform, error) {
	var target packageexec.Platform
	// Use the same authenticated connection settings as remote installation.
	if err := s.connectRemote(); err != nil {
		return target, err
	}
	err := s.request("GET", "/v1/packages/platform", nil, &target)
	return target, err
}

// compatiblePackages retains only artifacts advertised for the selected server platform.
func compatiblePackages(catalog githubpackages.Catalog, target packageexec.Platform) githubpackages.Catalog {
	result := githubpackages.Catalog{Repository: catalog.Repository, Version: catalog.Version, Packages: []githubpackages.AvailablePackage{}}
	// Inspect metadata only; discovery does not run or upload any executable.
	for _, item := range catalog.Packages {
		// Each package may advertise several OS and architecture combinations.
		for _, platform := range item.Platforms {
			// Include the exact server target without suggesting incompatible release assets.
			if platform == target {
				result.Packages = append(result.Packages, githubpackages.AvailablePackage{Name: item.Name, Platforms: []packageexec.Platform{target}})
			}
		}
	}
	return result
}
