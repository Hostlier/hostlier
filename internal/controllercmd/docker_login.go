package controllercmd

import (
	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
	runtimes "hostlier/internal/runtime"
)

// controllerRuntime attaches the installed Docker login to Docker and k3s application pulls.
func controllerRuntime(options runtimes.Options) (model.Runtime, error) {
	// k3s copies only the deployed registries' entries into the app namespace's pull secret.
	if options.Type == "docker" || options.Type == "" || options.Type == "k3s" {
		source, err := dockerlogin.InstalledSource(options.StateDir)
		// Invalid owner metadata prevents startup rather than selecting another account's login.
		if err != nil {
			return nil, err
		}
		options.DockerLogin = source
	}
	return runtimes.New(options)
}
