package runtime

import (
	"context"
	"encoding/json"
	"github.com/google/go-containerregistry/pkg/name"

	"net/url"
	"regexp"
	"runtime"

	"hostlier/internal/artifact"
	"hostlier/internal/dockerlogin"
	"hostlier/internal/model"
)

// RegistryCredential carries registry authentication and an explicitly approved token-service origin.
type RegistryCredential struct {
	Username           string `json:"username,omitempty"`
	IdentityToken      string `json:"identitytoken,omitempty"`
	RegistryToken      string `json:"registrytoken,omitempty"`
	Password           string `json:"password"`
	ServerAddress      string `json:"serveraddress,omitempty"`
	TokenServiceOrigin string `json:"-"`
}

// auth selects credentials for the image's explicit registry or the default Docker Hub registry.
func (r *Runtime) auth(ctx context.Context, image string) (RegistryCredential, bool, error) {
	registry := dockerlogin.Registry(image)
	c, ok := r.options.RegistryCredentials[registry]
	// Explicit embedding credentials retain precedence over the operator's Docker login.
	if ok {
		return c, true, nil
	}
	login, ok, err := r.options.DockerLogin.Lookup(ctx, registry)
	return RegistryCredential{Username: login.Username, Password: login.Password, IdentityToken: login.IdentityToken, RegistryToken: login.RegistryToken, ServerAddress: login.ServerAddress}, ok, err
}

// InspectImage resolves an immutable digest without pulling or modifying Docker images.
func (r *Runtime) InspectImage(ctx context.Context, image string) (model.ImageInfo, error) {
	// Prepared images are verified from retained content instead of a registry.
	if artifact.IsImage(image) {
		return r.inspectArtifact(image)
	}
	info, err := r.inspectRegistryImage(ctx, image)
	return info, registryImageContext(image, err)
}

// inspectRegistryImage resolves ordinary registry inputs independently of artifact storage.
func (r *Runtime) inspectRegistryImage(ctx context.Context, image string) (model.ImageInfo, error) {
	info := model.ImageInfo{Image: image, Warnings: []string{}}
	ref, err := name.ParseReference(image)
	// Reject malformed references before any local or remote lookup.
	if err != nil {
		return info, model.Err("IMAGE_RESOLUTION", "Invalid registry image")
	}
	// Tags can move. Always resolve them through the registry so a local image
	// cannot silently turn :latest into an older deployment.
	if _, pinned := ref.(name.Digest); pinned {
		// An immutable reference may use verified local platform metadata.
		if cached, ok := r.cachedImage(ctx, image); ok {
			return cached, nil
		}
	}
	return r.resolveRegistryManifest(ctx, image, info)
}

// registryToken validates credential routing before exchanging a registry challenge.
func (r *Runtime) registryToken(ctx context.Context, challenge, registry, repository, authorization string, credentials RegistryCredential, hasAuth bool) (string, error) {
	// Read only the quoted attributes required by the registry authentication exchange.
	field := func(key string) string { return challengeField(challenge, key) }
	tokenURL, e := url.Parse(field("realm"))
	// Require an HTTPS token-service URL before considering credential forwarding.
	if e != nil || tokenURL.Scheme != "https" {
		return "", model.Err("IMAGE_RESOLUTION", "Unsupported registry authentication challenge")
	}
	// Prevent credential delivery to a challenge-selected unapproved token origin.
	if hasAuth && !approvedTokenOrigin(tokenURL, registry, credentials.TokenServiceOrigin) {
		return "", model.Err("IMAGE_RESOLUTION", "Registry token service is not approved for credential delivery")
	}
	raw, err := r.exchangeRegistryToken(ctx, tokenURL, repository, field("service"), authorization, credentials, hasAuth)
	// Do not decode a token from a failed authentication request.
	if err != nil {
		return "", err
	}
	var token struct {
		Token       string
		AccessToken string `json:"access_token"`
	}
	// Require a structured token response rather than accepting arbitrary registry output.
	if json.Unmarshal(raw, &token) != nil {
		return "", model.Err("IMAGE_RESOLUTION", "Registry authentication failed")
	}
	// Support the standard access_token alias when token is absent.
	if token.Token == "" {
		token.Token = token.AccessToken
	}
	return token.Token, nil
}

// challengeField reads one quoted authentication attribute without accepting absent values.
func challengeField(challenge, key string) string {
	m := regexp.MustCompile(key + `="([^"]+)"`).FindStringSubmatch(challenge)
	// Return only a captured quoted attribute value.
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// fetchRegistryToken sends credentials only after the token-service origin is approved.
func (r *Runtime) fetchRegistryToken(ctx context.Context, tokenURL *url.URL, repository, service, authorization string, hasAuth bool) ([]byte, error) {
	query := tokenURL.Query()
	query.Set("scope", "repository:"+repository+":pull")
	// Preserve the challenge service audience when supplied by the registry.
	if v := service; v != "" {
		query.Set("service", v)
	}
	tokenURL.RawQuery = query.Encode()
	headers := map[string]string{}
	// Forward Basic credentials only after token-origin approval.
	if hasAuth {
		headers["Authorization"] = authorization
	}
	raw, e := request(ctx, registryClient(r.web), "GET", tokenURL.String(), nil, headers)
	// Normalize token transport errors without returning provider response content.
	if e != nil {
		return nil, model.Err("IMAGE_RESOLUTION", "Registry authentication failed")
	}
	return raw, nil
}

// cachedImage reports immutable local metadata and makes the freshness limitation explicit.
func (r *Runtime) cachedImage(ctx context.Context, image string) (model.ImageInfo, bool) {
	info := model.ImageInfo{Image: image}

	var local struct {
		RepoDigests      []string
		Os, Architecture string
	}
	// Only trust Docker's platform for a digest belonging to the requested repository.
	if r.api(ctx, "GET", "/images/"+url.PathEscape(image)+"/json", nil, &local, nil) != nil || local.Os != "linux" || local.Architecture != runtime.GOARCH {
		return info, false
	}
	ref, err := name.ParseReference(image)
	// Only compare canonical repository references.
	if err != nil {
		return info, false
	}
	// A local image can have digests belonging to several repositories.
	for _, candidate := range local.RepoDigests {
		d, err := name.NewDigest(candidate)
		// Another repository cannot authorize this pull.
		if err != nil || d.Context() != ref.Context() {
			continue
		}
		// Pinned requests require the exact recorded digest.
		if _, pinned := ref.(name.Digest); pinned && ref.Identifier() != d.Identifier() {
			continue
		}
		info.Digest = candidate
		info.Warnings = []string{"Using locally cached image verified for linux/" + runtime.GOARCH}
		return info, true
	}
	return info, false
}

// approvedTokenOrigin restricts credential forwarding to same-host or explicitly reviewed services.
func approvedTokenOrigin(target *url.URL, registry, approved string) bool {
	return target.User == nil && target.Fragment == "" && (target.Host == registry || (registry == "registry-1.docker.io" && target.Host == "auth.docker.io") || target.Scheme+"://"+target.Host == approved)
}

// registryImageContext identifies the failing image without exposing malformed credential-bearing input or provider bodies.
func registryImageContext(image string, err error) error {
	// Successful resolution never changes image metadata or approval identity.
	if err == nil {
		return nil
	}
	label := "supplied image"
	// A valid registry reference contains no URL userinfo or request credentials.
	if ref, parseErr := name.ParseReference(image); parseErr == nil {
		label = ref.Name()
	}
	problem, ok := err.(*model.Error)
	// Unknown transport/helper errors remain sanitized rather than disclosing arbitrary diagnostics.
	if !ok {
		return model.Err("IMAGE_RESOLUTION", "Cannot resolve "+label)
	}
	return model.Err(problem.Code, label+": "+problem.Message)
}
