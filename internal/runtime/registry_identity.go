package runtime

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"

	"hostlier/internal/model"
)

// registryClient prevents redirect-based delivery of credentials to an unreviewed origin.
func registryClient(base *http.Client) *http.Client {
	copy := *base
	// Never forward authorization headers or refresh-token bodies through redirects.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

// exchangeRegistryToken supports Docker device-login refresh tokens as well as username/token logins.
func (r *Runtime) exchangeRegistryToken(ctx context.Context, target *url.URL, repository, service, authorization string, c RegistryCredential, hasAuth bool) ([]byte, error) {
	// Password-based logins use the standard registry bearer challenge exchange.
	if c.IdentityToken == "" {
		return r.fetchRegistryToken(ctx, target, repository, service, authorization, hasAuth)
	}
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.IdentityToken}, "client_id": {"hostlier"}, "scope": {"repository:" + repository + ":pull"}, "service": {service}}
	req, err := http.NewRequestWithContext(ctx, "POST", target.String(), strings.NewReader(values.Encode()))
	// Invalid challenge targets must never receive the retained refresh token.
	if err != nil {
		return nil, model.Err("IMAGE_RESOLUTION", "Invalid registry token endpoint")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := registryClient(r.web).Do(req)
	// Provider details may contain credentials and must not be returned to callers.
	if err != nil {
		return nil, model.Err("IMAGE_RESOLUTION", "Registry identity-token exchange failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	// Bound token responses and require success; refresh tokens are never written back to Docker config.
	if err != nil || len(raw) > 1<<20 || response.StatusCode != http.StatusOK {
		return nil, model.Err("IMAGE_RESOLUTION", "Registry identity-token exchange failed")
	}
	return raw, nil
}

// registryAuthorization distinguishes ordinary passwords, identity tokens and ready-to-use registry tokens.
func registryAuthorization(c RegistryCredential, hasAuth bool) string {
	// A registry-issued bearer token does not require a password exchange.
	if c.RegistryToken != "" {
		return "Bearer " + c.RegistryToken
	}
	// Identity tokens must only be sent to an approved token endpoint in a refresh request.
	if !hasAuth || c.IdentityToken != "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password))
}

// anonymousManifestClient follows bounded HTTPS GET redirects while stripping all credential headers and cookies.
func anonymousManifestClient(base *http.Client) *http.Client {
	copy := *base
	copy.Jar = nil
	// Public registries commonly redirect manifest metadata to an authenticated-TLS mirror.
	copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		// Redirect targets cannot downgrade transport, introduce userinfo, change method or loop indefinitely.
		if len(via) >= 5 || request.URL.Scheme != "https" || request.URL.User != nil || request.Method != http.MethodGet {
			return model.Err("IMAGE_RESOLUTION", "Anonymous manifest redirect requires verified HTTPS GET and at most five requests")
		}
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		return nil
	}
	return &copy
}
