// Package engineoauth implements Hostlier's confidential management connection to Fused.
package engineoauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Metadata contains issuer-bound endpoints, never caller-selected token destinations.
type Metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

// Tokens are private controller credentials, never deployment environment variables.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// Client rejects redirects so credentials cannot escape the selected issuer.
func Client() *http.Client {
	// Reject redirects even when the destination appears related to Engine.
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Engine redirects are not allowed") }}
}

// Origin permits HTTPS and literal loopback HTTP for local development only.
func Origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	// Reject ambiguous authorities and URLs carrying credentials or query data.
	if err != nil || !validOrigin(u) {
		return "", errors.New("Engine URL must be an HTTPS origin")
	}
	ip := net.ParseIP(u.Hostname())
	// Remote engines must authenticate their TLS endpoint.
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return "", errors.New("Engine requires HTTPS")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Discover validates every endpoint before any private material is sent.
func Discover(ctx context.Context, origin string) (Metadata, error) {
	var m Metadata
	origin, err := Origin(origin)
	// Invalid origins cannot trigger network access.
	if err != nil {
		return m, err
	}
	err = Request(ctx, "GET", origin+"/.well-known/oauth-authorization-server", "", "", nil, &m)
	// Discovery must identify the exact selected issuer.
	if err != nil {
		return m, err
	}
	// A discovery document cannot substitute another issuer.
	if m.Issuer != origin {
		return m, errors.New("OAuth issuer mismatch")
	}
	// All back channels are pinned to the selected origin.
	for _, e := range []string{m.AuthorizationEndpoint, m.TokenEndpoint, m.RevocationEndpoint} {
		u, parseErr := url.Parse(e)
		// Reject invalid protocol state before advancing the connection.
		if parseErr != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme+"://"+u.Host != origin {
			return m, errors.New("OAuth endpoint outside selected Engine")
		}
	}
	return m, nil
}

// Request bounds responses and emits only status codes on upstream failures.
func Request(ctx context.Context, method, endpoint, bearer, contentType string, body io.Reader, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	// Reject malformed URLs before adding authorization.
	if err != nil {
		return errors.New("invalid Engine request")
	}
	// Attach management authority only to authenticated Engine operations.
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	// Set the explicit protocol encoding for request bodies.
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := Client().Do(req)
	// Transport errors may contain private callback URLs, so keep them out of API errors.
	if err != nil {
		return errors.New("Engine request failed")
	}
	defer response.Body.Close()
	// Report only the upstream status, never its potentially secret response body.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Engine returned HTTP %d", response.StatusCode)
	}
	// Bodyless success needs no response decoding.
	if result == nil {
		return nil
	}
	// Propagate failure before publishing any connection state.
	if err = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(result); err != nil {
		return errors.New("invalid Engine response")
	}
	return nil
}

// Exchange performs code redemption or refresh and validates usable bearer tokens.
func Exchange(ctx context.Context, endpoint string, form url.Values) (Tokens, error) {
	var t Tokens
	err := Request(ctx, "POST", endpoint, "", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), &t)
	// Missing or invalid token fields cannot become an authenticated connection.
	if err != nil {
		return t, err
	}
	// Reject invalid protocol state before advancing the connection.
	if t.AccessToken == "" || !strings.EqualFold(t.TokenType, "Bearer") || t.ExpiresIn <= 0 {
		return Tokens{}, errors.New("invalid OAuth token response")
	}
	return t, nil
}

// validOrigin rejects paths and ambient credentials before transport policy is evaluated.
func validOrigin(u *url.URL) bool {
	return u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}
