package cli

import (
	"path/filepath"

	"hostlier/internal/githubpackages"
	"hostlier/internal/model"
)

// sdk installs the standalone package-authoring SDK into the developer's current Go project.
func (s *session) sdk() error {
	if s.sub == "credential" && s.target == "export" {
		return s.exportSDKCredential()
	}
	// The bundled cloud client is installed locally without Go, npm or a registry lookup.
	if s.sub == "install" && s.target == "cloud" {
		return s.installCloudSDK()
	}
	if s.sub == "install" && s.target == "cloud-go" {
		return s.installCloudGoSDK()
	}
	// SDK setup is local development work, never an installation on a controller.
	if !s.validSDKCommand() {
		return model.Err("ARGUMENTS", "Usage: hostlier sdk install --version TAG_OR_COMMIT [--directory GO_PROJECT]")
	}
	// Resolve explicit versions before modifying go.mod or discovering credentials.
	if err := githubpackages.ValidateSelection(githubpackages.DefaultRepository, s.flags["version"]); err != nil {
		return err
	}
	directory := defaultValue(s.flags["directory"], s.cwd)
	// Relative project directories are resolved against the CLI invocation.
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(s.cwd, directory)
	}
	github := s.githubClient()
	commit, err := github.Commit(s.ctx, githubpackages.DefaultRepository, s.flags["version"])
	// A missing or inaccessible ref cannot fall back to a moving repository head.
	if err != nil {
		return err
	}
	install := s.options.InstallSDK
	// Production uses Go modules with an invocation-scoped credential helper.
	if install == nil {
		install = githubpackages.InstallSDK
	}
	// Success requires the pinned dependency operation to complete.
	if err = install(s.ctx, directory, commit, github.Token); err != nil {
		return err
	}
	return s.print(map[string]string{"module": githubpackages.SDKModule, "commit": commit, "directory": directory, "import": githubpackages.SDKModule + "/sdk/packagekit"})
}

// validSDKCommand confines SDK installation to one explicit local Go project operation.
func (s *session) validSDKCommand() bool {
	return s.sub == "install" && len(s.parsed.args) == 2 && s.flags["server"] == "" && s.flags["socket"] == "" && s.flags["github"] == "" && s.flags["repository"] == "" && s.flags["on"] == ""
}
