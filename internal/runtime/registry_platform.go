package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
)

// platformLookup reads immutable registry metadata with the controller's existing credential policy.
type platformLookup struct {
	r                             *Runtime
	registry, repo, authorization string
	credential                    RegistryCredential
	authenticated                 bool
}

const manifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// resolveRegistryManifest selects a Linux child for this controller and verifies the actual image config before approval.
func (r *Runtime) resolveRegistryManifest(ctx context.Context, image string, info model.ImageInfo) (model.ImageInfo, error) {
	ref, err := name.ParseReference(image)
	// Do not send malformed repository coordinates to a registry.
	if err != nil {
		return info, model.Err("IMAGE_RESOLUTION", "Invalid registry image")
	}
	reg, repo := ref.Context().RegistryStr(), ref.Context().RepositoryStr()
	// Hub reference names use a different host from the registry API.
	if dockerlogin.Canonical(reg) == "docker.io" {
		reg = "registry-1.docker.io"
	}
	cred, authed, err := r.auth(ctx, image)
	// Credential lookup failures must not downgrade a private lookup to anonymous.
	if err != nil {
		return info, err
	}
	session := platformLookup{r: r, registry: reg, repo: repo, credential: cred, authenticated: authed, authorization: registryAuthorization(cred, authed)}
	digest, err := session.image(ctx, ref.Identifier())
	// Only verified content may become a planned digest.
	if err != nil {
		return info, err
	}
	// Use the pullable Hub name in the revision.
	if reg == "registry-1.docker.io" {
		reg = "docker.io"
	}
	info.Digest = reg + "/" + repo + "@" + digest
	return info, nil
}

// image follows only matching descriptors, then checks config metadata even for single-manifest pins.
func (s *platformLookup) image(ctx context.Context, ref string) (string, error) {
	// Bound nested index traversal even for a malicious registry.
	for depth := 0; depth < 8; depth++ {
		body, digest, err := s.read(ctx, "manifests", ref)
		// Require the manifest bytes addressed by this descriptor.
		if err != nil {
			return "", err
		}
		var manifest struct {
			SchemaVersion int
			Config        v1.Descriptor
			Manifests     []v1.Descriptor
		}
		// Only schema 2 image manifests and indexes carry the required platform metadata.
		if json.Unmarshal(body, &manifest) != nil || manifest.SchemaVersion != 2 {
			return "", model.Err("IMAGE_RESOLUTION", "Invalid registry manifest")
		}
		// A leaf manifest needs a config check regardless of what its parent advertised.
		if manifest.Manifests == nil {
			return digest, s.checkConfig(ctx, manifest.Config.Digest.String())
		}
		ref, err = serverManifest(manifest.Manifests)
		// Never substitute an unrelated manifest when the requested platform is absent.
		if err != nil {
			return "", err
		}
	}
	return "", model.Err("IMAGE_RESOLUTION", "Too many nested registry indexes")
}

// serverManifest skips unrelated platforms and provenance descriptors in an index.
func serverManifest(entries []v1.Descriptor) (string, error) {
	// An index may contain other architectures and build attestations.
	for _, entry := range entries {
		p := entry.Platform
		// Select a descriptor labeled with the controller platform and check its config next.
		if p != nil && p.OS == "linux" && p.Architecture == runtime.GOARCH {
			return entry.Digest.String(), nil
		}
	}
	return "", model.Err("IMAGE_PLATFORM", "Image has no manifest for linux/"+runtime.GOARCH)
}

// checkConfig rejects mismatched and missing platform fields before a digest is returned for use.
func (s *platformLookup) checkConfig(ctx context.Context, digest string) error {
	body, _, err := s.read(ctx, "blobs", digest)
	// The platform must come from the manifest’s verified config blob.
	if err != nil {
		return err
	}
	var cfg v1.ConfigFile
	// Invalid config metadata cannot establish platform compatibility.
	if json.Unmarshal(body, &cfg) != nil {
		return model.Err("IMAGE_RESOLUTION", "Invalid registry image config")
	}
	// A foreign OS or architecture would otherwise fail at container startup.
	if cfg.OS != "linux" || cfg.Architecture != runtime.GOARCH {
		return model.Err("IMAGE_PLATFORM", fmt.Sprintf("Image is %s/%s; controller requires linux/%s", cfg.OS, cfg.Architecture, runtime.GOARCH))
	}
	return nil
}

// read bounds metadata and verifies every digest link, including redirected config blobs.
func (s *platformLookup) read(ctx context.Context, kind, ref string) ([]byte, string, error) {
	resp, err := s.request(ctx, kind, ref)
	// Preserve sanitized transport and authentication errors.
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	// An error page cannot establish an image identity.
	if resp.StatusCode != http.StatusOK {
		return nil, "", model.Err("IMAGE_RESOLUTION", fmt.Sprintf("Registry metadata lookup returned HTTP %d", resp.StatusCode))
	}
	const limit = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	// Reject oversized metadata before decoding it.
	if err != nil || len(body) > limit {
		return nil, "", model.Err("IMAGE_RESOLUTION", "Invalid registry metadata size")
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	// Tags resolve to a new digest, while immutable references must match their bytes.
	if ref != digest && (kind == "blobs" || strings.Contains(ref, ":")) {
		return nil, "", model.Err("IMAGE_RESOLUTION", "Registry content disagrees with the pinned digest")
	}
	// An advertised identity must agree with the body the plan actually verified.
	if header := resp.Header.Get("Docker-Content-Digest"); header != "" && header != digest {
		return nil, "", model.Err("IMAGE_RESOLUTION", "Registry metadata digest disagrees with response")
	}
	return body, digest, nil
}

// request exchanges a challenge only with approved credential recipients.
func (s *platformLookup) request(ctx context.Context, kind, ref string) (*http.Response, error) {
	// A registry ref belongs to one path component.
	if strings.ContainsAny(ref, "/?#") || ref == "" {
		return nil, model.Err("IMAGE_RESOLUTION", "Invalid registry metadata reference")
	}
	target := "https://" + s.registry + "/v2/" + s.repo + "/" + kind + "/" + ref

	resp, err := s.get(ctx, target, kind)
	// Only an authentication challenge warrants a second registry request.
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	resp.Body.Close()
	token, err := s.r.registryToken(ctx, resp.Header.Get("WWW-Authenticate"), s.registry, s.repo, registryAuthorization(s.credential, s.authenticated), s.credential, s.authenticated)
	// Token exchange errors must not retry with weaker credentials.
	if err != nil {
		return nil, err
	}
	s.authorization = "Bearer " + token
	return s.get(ctx, target, kind)
}

// get fetches metadata once and strips credentials before following any blob redirect.
func (s *platformLookup) get(ctx context.Context, target, kind string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	// Reject invalid endpoint construction before attaching credentials.
	if err != nil {
		return nil, model.Err("IMAGE_RESOLUTION", "Invalid registry endpoint")
	}
	req.Header.Set("Accept", manifestTypes)
	// Anonymous registries receive no credential header.
	if s.authorization != "" {
		req.Header.Set("Authorization", s.authorization)
	}
	client := registryClient(s.r.web)
	// Config blobs can redirect to signed CDN URLs. Never carry credentials to the redirect.
	if kind == "blobs" || s.authorization == "" {
		client = anonymousManifestClient(s.r.web)
	}
	resp, err := client.Do(req)
	// Registry transport errors may contain signed private URLs.
	if err != nil {
		return nil, model.Err("IMAGE_RESOLUTION", "Registry metadata request failed")
	}
	return resp, nil
}
