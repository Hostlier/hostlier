package cli

import (
	"fmt"
	"net/url"

	"hostlier/internal/githubpackages"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
)

// remotePackages manages the explicitly selected controller through its existing authenticated transport.
func (s *session) remotePackages() error {
	// Validate target and source intent before opening the remote connection.
	if err := s.validateRemotePackages(); err != nil {
		return err
	}
	// Profile or TLS failures must never fall back to a local installation.
	if err := s.connectRemote(); err != nil {
		return err
	}
	// Remote inventory does not execute or install anything.
	if s.sub == "list" && len(s.parsed.args) == 2 {
		return s.getPrint("/v1/packages")
	}
	// Explicit discovery selects one installed remote package.
	if s.sub == "describe" && len(s.parsed.args) == 3 {
		return s.getPrint("/v1/packages/" + url.PathEscape(s.target) + "/describe")
	}
	// Only the validated installation operation may upload bytes.
	if s.sub != "install" {
		return model.Err("ARGUMENTS", packageUsage)
	}
	return s.installRemotePackage()
}

// installRemotePackage downloads or reads on the CLI, stages on the server, then requests exact human approval.
func (s *session) installRemotePackage() error {
	var target packageexec.Platform
	// Server architecture, not laptop architecture, selects the release artifact.
	if err := s.request("GET", "/v1/packages/platform", nil, &target); err != nil {
		return err
	}
	download, err := s.remotePackageSource(target)
	// Source validation happens before any server-side upload.
	if err != nil {
		return err
	}
	// Uploading inert bytes is distinct from granting executable installation authority.
	if err = s.uploadPackage(download); err != nil {
		return err
	}
	var installed packageexec.Installation
	// Existing approval handling binds the owner decision to this name, digest and source metadata.
	if err = s.request("POST", "/v1/packages", download.Request, &installed); err != nil {
		return err
	}
	// A remote success must identify the exact bytes the CLI requested.
	if installed.Name != download.Request.Name || installed.Digest != download.Request.Digest {
		return model.Err("PACKAGE_IDENTITY", "Controller returned a different installed package identity")
	}
	return s.print(installed)
}

// remotePackageSource never sends GitHub credentials or laptop filesystem paths to the controller.
func (s *session) remotePackageSource(target packageexec.Platform) (githubpackages.Download, error) {
	// Release lookup and download stay on the authenticated CLI machine.
	if s.flags["file"] == "" {
		return s.packageDownload(target)
	}
	data, err := packageexec.ReadSource(s.packageFile())
	// Unsafe local inputs cannot be converted into privileged remote uploads.
	if err != nil {
		return githubpackages.Download{}, err
	}
	// User-supplied files must also match the actual server architecture.
	if err = packageexec.CheckPlatform(data, target); err != nil {
		return githubpackages.Download{}, err
	}
	request := packageexec.InstallRequest{Name: s.target, Digest: packageexec.Digest(data), Size: int64(len(data))}
	return githubpackages.Download{Request: request, Data: data}, nil
}

// uploadPackage transfers bounded replayable chunks without executing or activating their content.
func (s *session) uploadPackage(download githubpackages.Download) error {
	// Every chunk has a deterministic identity for connection-loss retries.
	for offset := 0; offset < len(download.Data); offset += packageexec.ChunkBytes {
		end := min(offset+packageexec.ChunkBytes, len(download.Data))
		body := map[string]any{"offset": offset, "data": download.Data[offset:end], "requestId": fmt.Sprintf("%s-%d", download.Request.Digest, offset)}
		var response struct {
			Offset int64 `json:"offset"`
		}
		// Exact chunk replays are checked by the server against the retained prefix.
		if err := s.request("POST", "/v1/package-uploads/"+download.Request.Digest+"/chunks", body, &response); err != nil {
			return err
		}
		// Reject incomplete or impossible acknowledgements before requesting activation.
		if response.Offset < int64(end) || response.Offset > download.Request.Size {
			return model.Err("PACKAGE_UPLOAD", "Controller did not retain the expected package bytes")
		}
	}
	return nil
}

// validateRemotePackages rejects conflicting targets and invalid installation intent.
func (s *session) validateRemotePackages() error {
	// Local state paths must not silently redirect a remote package request.
	if s.flags["state-dir"] != "" || (s.flags["server"] != "" && s.flags["socket"] != "") {
		return model.Err("ARGUMENTS", "Select one remote --server or --socket; --state-dir is for local installation only")
	}
	// Invalid installation options are rejected before opening a connection.
	if s.sub == "install" {
		// Installation source validation precedes network access.
		if err := s.validatePackageInstall(); err != nil {
			return err
		}
	}
	return nil
}
