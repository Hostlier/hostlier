package cli

import (
	"errors"
	"fmt"

	"hostlier/internal/artifact"
	"hostlier/internal/model"
)

// resolveContainerArchitecture captures the selected receiver once for all primary and sidecar sources.
func (s *session) resolveContainerArchitecture(requested string) (string, error) {
	// The normal deployment path is already connected; direct preparation may need its saved profile.
	if s.remote == nil {
		if err := s.connectRemote(); err != nil { // No controller means no inferred platform or download.
			return "", err
		}
	}
	// Cache only within this invocation, whose selected fleet host cannot change mid-preparation.
	if s.containerArchitecture == "" {
		var platform struct{ OS, Arch string }
		if err := s.request("GET", "/v1/artifacts/platform", nil, &platform); err != nil { // Discovery failures cannot choose the laptop CPU.
			return "", err
		}
		if platform.OS != "linux" || (platform.Arch != "amd64" && platform.Arch != "arm64") { // Only implemented container targets are allowed.
			return "", model.Err("ARTIFACT_PLATFORM", "Controller must target Linux amd64 or arm64 containers")
		}
		s.containerArchitecture = platform.Arch
	}
	// Retain an explicit project architecture as a constraint, including on sidecars.
	if requested != "" && requested != s.containerArchitecture {
		return "", model.Err("ARTIFACT_PLATFORM", "Container architecture does not match controller linux/"+s.containerArchitecture)
	}
	return s.containerArchitecture, nil
}

// prepareControllerImage requests inert registry staging before consulting any laptop registry login.
func (s *session) prepareControllerImage(source artifact.RegistryImage) (artifact.Result, bool, error) {
	fmt.Fprintf(s.errOut, "Asking controller to download %s for linux/%s\n", source.Image, source.Architecture)
	var receipt artifact.Result
	body := map[string]any{"image": source.Image, "architecture": source.Architecture}
	err := s.request("POST", "/v1/artifacts/pull", body, &receipt)
	// A successful preparation must name verified content, never a mutable registry tag.
	if err == nil {
		if !artifact.ValidReceipt(receipt) { // Malformed success replies cannot authorize a different source.
			return receipt, false, model.Err("RESPONSE", "Controller returned an invalid prepared image")
		}
		return receipt, false, nil
	}
	var failure *model.Error
	// Only registry access failures permit downloading with a different local credential source.
	if !errors.As(err, &failure) || !registryDownloadFallback(failure.Code) {
		return receipt, false, err
	}
	fmt.Fprintln(s.errOut, "Controller could not download the registry image; trying local credentials and upload")
	return artifact.Result{}, true, nil
}

// registryDownloadFallback excludes controller authentication, platform, storage and ambiguous transport failures.
func registryDownloadFallback(code string) bool {
	return code == "IMAGE_PULL" || code == "IMAGE_RESOLUTION" || code == "REGISTRY_CREDENTIALS"
}
