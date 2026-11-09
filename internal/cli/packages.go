package cli

import (
	"path/filepath"
	"runtime"

	"hostlier/internal/githubpackages"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
)

const packageUsage = "Usage: hostlier package add NAME; package install NAME (--file BINARY | --repository GIT_URL --version TAG_OR_COMMIT) [--server NAME | --state-dir DIR]; package list [--server NAME]; package describe NAME [--server NAME]; package available [--repository GIT_URL | --github OWNER/REPO] [--version TAG] [--server NAME]"

// packages selects explicit connected-server management or the original local administrator commands.
func (s *session) packages() error {
	// Enrollment edits project metadata before any package inventory or remote selection.
	if s.sub == "add" {
		return s.addProjectPackage()
	}
	// Fleet installation requires a connection to the target controller rather than ambiguous implicit routing.
	if s.flags["on"] != "" {
		return model.Err("ARGUMENTS", "Connect directly to the package host with --server NAME")
	}
	// Repository discovery is distinct from installed-package inventory.
	if s.availablePackageCommand() {
		return s.availablePackages()
	}
	// Explicit remote targets must never silently write to this machine's local package directory.
	if s.flags["server"] != "" || s.flags["socket"] != "" {
		return s.remotePackages()
	}
	return s.localPackages()
}

// localPackages preserves offline installation and inventory for administrator-owned files.
func (s *session) localPackages() error {
	runner := s.localInstallation()
	// Inventory measures local artifacts without contacting a controller.
	if s.sub == "list" && len(s.parsed.args) == 2 {
		items, err := runner.Installed()
		// Failed local package operations must not print success.
		if err != nil {
			return err
		}
		return s.print(items)
	}
	// Explicit local discovery executes only a previously installed package.
	if s.sub == "describe" && len(s.parsed.args) == 3 {
		selected, err := s.localPackageRunner(s.target)
		if err != nil {
			return err
		}
		capability, err := selected.Describe(s.ctx, s.target)
		// Failed local package operations must not print success.
		if err != nil {
			return err
		}
		return s.print(capability)
	}
	return s.installPackage(runner)
}

// validatePackageInstall requires exactly one explicit package source and rejects broad approval bypasses.
func (s *session) validatePackageInstall() error {
	// Remote executable authority must remain bound to one package identity.
	if s.sub != "install" || len(s.parsed.args) != 3 || !packageexec.ValidName(s.target) {
		return model.Err("ARGUMENTS", packageUsage)
	}
	// --yes is reserved for server initialization, including when installing packages remotely.
	if s.flags["yes"] != "" {
		return model.Err("APPROVAL_REQUIRED", "--yes is not supported for package installation")
	}
	fromFile := s.flags["file"] != ""
	fromGitHub := s.flags["github"] != "" || s.flags["repository"] != "" || s.flags["version"] != ""
	// A caller cannot mix local content with release provenance or omit the source.
	if fromFile == fromGitHub {
		return model.Err("ARGUMENTS", packageUsage)
	}
	// Repository transports are mutually exclusive and require an explicit installation revision.
	if fromGitHub {
		return s.validatePackageRepository(true)
	}
	return nil
}

// installPackage preserves offline file installation and adds verified GitHub release artifacts.
func (s *session) installPackage(runner packageexec.Runner) error {
	// Validate before accessing either local files or GitHub.
	if err := s.validatePackageInstall(); err != nil {
		return err
	}
	// Explicit local binary installation keeps its existing administrator-owned file checks.
	if s.flags["file"] != "" {
		item, err := runner.Install(s.target, s.packageFile())
		// Failed local package operations must not print success.
		if err != nil {
			return err
		}
		return s.print(item)
	}
	download, err := s.packageDownload(packageexec.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH})
	// Only a complete checksum-verified native artifact can be activated locally.
	if err != nil {
		return err
	}
	item, err := runner.InstallVerified(download.Request, download.Data)
	// Failed local installation must not print success.
	if err != nil {
		return err
	}
	return s.print(item)
}

// packageFile resolves an explicitly selected binary relative to this invocation's working directory.
func (s *session) packageFile() string {
	source := s.flags["file"]
	// Absolute source paths retain their administrator-selected identity.
	if !filepath.IsAbs(source) {
		source = filepath.Join(s.cwd, source)
	}
	return source
}

// githubClient keeps repository access entirely on the CLI machine.
func (s *session) githubClient() *githubpackages.Client {
	// Tests may supply an isolated GitHub API fixture.
	if s.options.GitHub != nil {
		return s.options.GitHub
	}
	return githubpackages.New(githubpackages.Credentials(s.ctx))
}

// packageDownload chooses exactly one release artifact for the receiving machine.
func (s *session) packageDownload(target packageexec.Platform) (githubpackages.Download, error) {
	// Generic Git repositories carry a catalog independent of hosting-provider release APIs.
	if repository := s.packageRepository(); repository != "" {
		return s.gitClient().Package(s.ctx, repository, s.flags["version"], s.target, target)
	}
	repository := s.flags["github"]
	// Invalid coordinates should not trigger local credential discovery.
	if err := githubpackages.ValidateSelection(repository, s.flags["version"]); err != nil {
		return githubpackages.Download{}, err
	}
	return s.githubClient().Package(s.ctx, repository, s.flags["version"], s.target, target)
}
