package installer

import (
	"fmt"
	"runtime"
	"strings"

	"hostlier/internal/dockerlogin"
)

// resolveDockerLogin retains the reviewed login owner across sudo and later root-only retries.
func resolveDockerLogin(o Options, installed *Installation) (Options, error) {
	// Only the two supported runtimes pull application images with the selected login.
	if !loginRuntime(o.Runtime) {
		o.DockerLogin = nil
		return o, nil
	}
	// Durable account selection wins over a different operator performing a retry.
	if installed != nil && installed.DockerLogin != nil {
		o.DockerLogin = installed.DockerLogin
	}
	// Resolve the invoking account only on a Linux server and only on first selection.
	if o.DockerLogin == nil && runtime.GOOS == "linux" {
		source, err := dockerlogin.Discover()
		// Identity errors must be reviewed before any installation mutation.
		if err != nil {
			return o, err
		}
		o.DockerLogin = source
	}
	return o, nil
}

// dockerLoginAssets exposes only the selected login home and store context as read-only mounts.
func dockerLoginAssets(o Options, a Assets) (Assets, error) {
	// Installations without a selected login keep their original home isolation.
	if !loginRuntime(o.Runtime) || o.DockerLogin == nil {
		return a, nil
	}
	source := o.DockerLogin
	// Reject unit injection before interpolating reviewed paths or numeric account IDs.
	if err := source.Validate(); err != nil {
		return Assets{}, err
	}
	mounts := fmt.Sprintf("ProtectHome=tmpfs\nBindReadOnlyPaths=-%s -%s -/run/user/%s", source.Home, source.Directory, source.UID)
	a.Service = strings.Replace(a.Service, "ProtectHome=true", mounts, 1)
	return a, nil
}

// loginRuntime reports whether a runtime reads the server account's Docker login for image pulls.
func loginRuntime(runtime string) bool { return runtime == "docker" || runtime == "k3s" }

// DockerLoginSource supplies only persisted local metadata to the running Docker controller.
func DockerLoginSource(stateDir string) (*dockerlogin.Source, error) {
	return dockerlogin.InstalledSource(stateDir)
}
