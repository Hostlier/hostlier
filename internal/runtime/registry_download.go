package runtime

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"

	challenge "github.com/docker/distribution/registry/client/auth/challenge"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"hostlier/internal/artifact"
	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
)

// PrepareRegistryImage downloads immutable content with the server's configured login for either backend.
func (r *Runtime) PrepareRegistryImage(ctx context.Context, image, dir string) (artifact.Result, error) {
	info, err := r.InspectImage(ctx, image)
	// Resolve the server platform and credential policy before attempting filesystem-layer downloads.
	if err != nil {
		return artifact.Result{}, err
	}
	credential, authenticated, err := r.auth(ctx, info.Digest)
	// Login changes and helper failures never downgrade to anonymous downloads.
	if err != nil {
		return artifact.Result{}, err
	}
	ref, err := name.ParseReference(info.Digest)
	// Only the verified registry digest may drive the actual content download.
	if err != nil {
		return artifact.Result{}, model.Err("IMAGE_RESOLUTION", "Invalid resolved registry image")
	}
	registry := ref.Context().RegistryStr()
	// Docker Hub's token service is shared by its historical registry aliases.
	if dockerlogin.Canonical(registry) == "docker.io" {
		registry = "registry-1.docker.io"
	}
	base := r.web.Transport
	// Runtime transports may use the standard verified TLS transport.
	if base == nil {
		base = http.DefaultTransport
	}
	transport := registryDownloadTransport{base: base, registry: registry, credential: credential, authenticated: authenticated}
	auth := authn.FromConfig(authn.AuthConfig{Username: credential.Username, Password: credential.Password, IdentityToken: credential.IdentityToken, RegistryToken: credential.RegistryToken})
	source := artifact.RegistryImage{Image: info.Digest, Architecture: goruntime.GOARCH}
	path := filepath.Join(dir, "registry.tar")
	// Download all bytes before returning an image that the controller may plan or retain.
	reserve := artifact.Store{Root: dir}
	// Streaming writes preserve the same free-space reserve as artifact uploads.
	if err := artifact.DownloadRegistry(ctx, source, path, auth, transport, &reserve); err != nil {
		return artifact.Result{}, err
	}
	file, err := os.Stat(path)
	// Canonical packaging temporarily needs two more copies of the downloaded archive.
	if err != nil {
		return artifact.Result{}, model.Err("ARTIFACT_WRITE", "Cannot inspect downloaded archive")
	}
	if err := reserve.CheckAvailableSpace(2 * file.Size()); err != nil { // Preserve reserve through archive normalization too.
		return artifact.Result{}, err
	}
	return artifact.PrepareContainer(dir, path, source.Architecture)
}

// PrepareRegistryImage uses the same registry policy for k3s without requiring Docker or creating a Pod.
func (k *K3s) PrepareRegistryImage(ctx context.Context, image, dir string) (artifact.Result, error) {
	return k.base.PrepareRegistryImage(ctx, image, dir)
}

// registryDownloadTransport keeps native layer downloads inside the runtime's credential origin policy.
type registryDownloadTransport struct {
	base          http.RoundTripper
	registry      string
	credential    RegistryCredential
	authenticated bool
}

// RoundTrip rejects credential-bearing token redirects and strips authentication from external CDN reads.
func (t registryDownloadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// Downloads cannot downgrade TLS or introduce URL userinfo, even through registry redirects.
	if request.URL.Scheme != "https" || request.URL.User != nil {
		return nil, model.Err("IMAGE_PULL", "Registry download requires verified HTTPS")
	}
	copy := request.Clone(request.Context())
	// Only explicitly approved registry/token origins may receive passwords, tokens or refresh bodies.
	if !t.approvedOrigin(copy.URL) {
		if copy.Method != http.MethodGet && copy.Method != http.MethodHead { // External URLs are only anonymous content reads.
			return nil, model.Err("IMAGE_PULL", "Registry token origin is not approved")
		}
		copy.Header.Del("Authorization")
	}
	copy.Header.Del("Cookie")
	response, err := t.base.RoundTrip(copy)
	// Failed transports cannot supply a registry authentication challenge.
	if err != nil {
		return nil, err
	}
	// Check challenges before the library can forward a password or refresh token to their realm.
	if t.authenticated && !t.approvedChallenges(response) {
		response.Body.Close()
		return nil, model.Err("IMAGE_PULL", "Registry token origin is not approved")
	}
	return response, nil
}

// approvedOrigin also admits Docker Hub's historical API hostname used by the native registry library.
func (t registryDownloadTransport) approvedOrigin(target *url.URL) bool {
	return approvedTokenOrigin(target, t.registry, t.credential.TokenServiceOrigin) || (t.registry == "registry-1.docker.io" && target.Host == "index.docker.io")
}

// approvedChallenges applies the same reviewed token-service rule used during tag resolution.
func (t registryDownloadTransport) approvedChallenges(response *http.Response) bool {
	// Multiple challenges must all keep credential delivery within approved HTTPS origins.
	for _, value := range challenge.ResponseChallenges(response) {
		realm := value.Parameters["realm"]
		if realm == "" { // Basic authentication has no separate token service.
			continue
		}
		target, err := url.Parse(realm)
		if err != nil || target.Scheme != "https" || !approvedTokenOrigin(target, t.registry, t.credential.TokenServiceOrigin) { // Never trust a registry-selected arbitrary origin.
			return false
		}
	}
	return true
}
