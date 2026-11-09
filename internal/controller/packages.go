package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"runtime"

	"hostlier/internal/model"
	"hostlier/internal/packageexec"
)

// packageRunner owns optional executables under this controller's fixed state directory.
func (c *Controller) packageRunner() packageexec.Runner {
	return packageexec.Runner{Directory: filepath.Join(c.opts.StateDir, "packages")}
}

// packageUploads isolates inert binary staging from runnable package installations.
func (c *Controller) packageUploads() packageexec.Uploads {
	return packageexec.Uploads{Root: filepath.Join(c.opts.StateDir, "package-uploads")}
}

// packagePlatform reports the receiver architecture used for release asset selection.
func packagePlatform() packageexec.Platform {
	return packageexec.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// apiPackageUploads accepts bounded, actor-owned bytes without executable permissions or activation.
func (c *Controller) apiPackageUploads(r *http.Request, p []string, actor string) (any, error) {
	// Only the exact preparation endpoint bypasses independent installation approval.
	if !matches(r, p, "POST", 4, "chunks") {
		return endpointNotFound()
	}
	var chunk artifactChunkRequest
	// Reject unknown fields and oversized JSON before writing any staging bytes.
	if err := decode(r, &chunk); err != nil {
		return nil, err
	}
	offset, err := c.packageUploads().Chunk(actor, p[2], chunk.Offset, chunk.Data)
	// Untrusted upload errors need a safe, actionable public type.
	if err != nil {
		return nil, model.Err("PACKAGE_UPLOAD", err.Error())
	}
	return map[string]int64{"offset": offset}, nil
}

// apiPackages exposes remote inventory and exactly approved executable activation.
func (c *Controller) apiPackages(r *http.Request, p []string, actor string) (any, error) {
	// Platform discovery never runs installed package code.
	if matches(r, p, "GET", 3, "platform") {
		return packagePlatform(), nil
	}
	// Listing measures executable digests without invoking them.
	if matches(r, p, "GET", 2, "") {
		return c.packageRunner().Installed()
	}
	// Explicit discovery runs a previously installed, administrator-approved executable.
	if matches(r, p, "GET", 4, "describe") {
		return c.packageRunner().Describe(r.Context(), p[2])
	}
	// Mutations reach this handler only through local administration or consumed human approval.
	if matches(r, p, "POST", 2, "") {
		var request packageexec.InstallRequest
		// Installation accepts exact metadata, never a server filesystem destination or download credential.
		if err := decode(r, &request); err != nil {
			return nil, err
		}
		return c.installUploadedPackage(request, actor)
	}
	return endpointNotFound()
}

// checkedPackage rehashes actor-owned staging and independently verifies the server's native architecture.
func (c *Controller) checkedPackage(request packageexec.InstallRequest, actor string) ([]byte, error) {
	data, err := c.packageUploads().Read(actor, request)
	// Missing or changed uploads cannot be reviewed or activated.
	if err != nil {
		return nil, model.Err("PACKAGE_UPLOAD", err.Error())
	}
	// The server independently verifies the executable rather than trusting the CLI's asset label.
	if err = packageexec.CheckPlatform(data, packagePlatform()); err != nil {
		return nil, model.Err("PACKAGE_PLATFORM", err.Error())
	}
	return data, nil
}

// installUploadedPackage activates verified bytes and invalidates stale deployment approvals.
func (c *Controller) installUploadedPackage(request packageexec.InstallRequest, actor string) (any, error) {
	data, err := c.checkedPackage(request, actor)
	// Approval cannot substitute bytes or claim another actor's staging upload.
	if err != nil {
		return nil, err
	}
	installed, err := c.packageRunner().InstallVerified(request, data)
	// Failed installation must retain staging for diagnosis and never claim success.
	if err != nil {
		return nil, model.Err("PACKAGE_INSTALL", err.Error())
	}
	c.state.Generation++
	c.packageUploads().Remove(actor, request.Digest)
	c.log("Package installed: "+installed.Name+" sha256:"+installed.Digest, "", "info")
	return installed, nil
}

// humanPackageReview displays server-verified executable identity before granting root-level installation authority.
func (c *Controller) humanPackageReview(request humanRequest, actor string, review map[string]any) (json.RawMessage, error) {
	var installation packageexec.InstallRequest
	// Reuse strict request decoding for the same metadata executed after approval.
	r := &http.Request{Body: io.NopCloser(bytes.NewReader(request.Body))}
	// Malformed approval payloads cannot produce a partial or misleading executable review.
	if err := decode(r, &installation); err != nil {
		return nil, err
	}
	// Rehash actual staging bytes before showing their claimed identity to the owner.
	if _, err := c.checkedPackage(installation, actor); err != nil {
		return nil, err
	}
	review["package"] = installation
	review["platform"] = packagePlatform()
	review["warning"] = "Install this exact executable on the controller. Packages run with controller privileges. Source coordinates are supplied by the requesting client; the server verifies the displayed SHA-256 and target platform."
	return json.Marshal(review)
}

// packageInstallRequest identifies the exact privileged executable-installation operation.
func packageInstallRequest(request humanRequest) bool {
	return request.Method == "POST" && request.Path == "/v1/packages"
}
