package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hostlier/internal/engineoauth"
	"hostlier/internal/model"
	"hostlier/internal/store"
)

// FusedOAuthConnection persists only public metadata and an authenticated ciphertext.
type FusedOAuthConnection struct {
	EngineURL       string                 `json:"engineUrl"`
	Status          string                 `json:"status"`
	ExpiresAt       time.Time              `json:"expiresAt"`
	Ciphertext      string                 `json:"ciphertext"`
	Runtime         *model.FusedConnection `json:"runtime,omitempty"`
	ManifestDigest  string                 `json:"manifestDigest,omitempty"`
	AppFamilyID     string                 `json:"appFamilyId,omitempty"`
	DefaultBucketID string                 `json:"defaultBucketId,omitempty"`
	Buckets         []model.FusedBucket    `json:"buckets,omitempty"`
}

// fusedOAuthPrivate keeps client credentials, PKCE and tokens out of public responses.
type fusedOAuthPrivate struct {
	Actor           string
	ClientExpiresAt time.Time
	Metadata        engineoauth.Metadata
	ClientID        string
	ClientSecret    string
	StateHash       string
	Verifier        string
	RedirectURI     string
	Tokens          engineoauth.Tokens
	Runtime         *fusedRuntimeConsent
}

// apiFused routes owner-approved connection operations; deployment grants cannot enter this namespace.
func (c *Controller) apiFused(r *http.Request, p []string, actor string) (any, error) {
	// Collection reads deliberately omit encrypted payloads and client credentials.
	if r.Method == "GET" && len(p) == 2 {
		return c.fusedStatuses(r)
	}
	// Accept only a single canonical connection name.
	if len(p) < 3 || !scopeName.MatchString(p[2]) {
		return endpointNotFound()
	}
	name := p[2]
	// Reject methods or paths outside this exact protocol route.
	if r.Method == "DELETE" && len(p) == 3 {
		return c.disconnectFused(r, name)
	}
	// Reject methods or paths outside this exact protocol route.
	if r.Method != "POST" || len(p) != 4 {
		return endpointNotFound()
	}
	// Each mutation inherits Hostlier's exact-request approval boundary.
	return c.fusedMutation(r, p[3], name, actor)
}

// fusedMutation dispatches only recognized owner-approved connection actions.
func (c *Controller) fusedMutation(r *http.Request, action, name, actor string) (any, error) {
	// Reject invalid protocol state before advancing the connection.
	switch action {
	// Handle only this explicitly supported connection action.
	case "connect":
		return c.beginFused(r, name, actor)
	// Handle only this explicitly supported connection action.
	case "complete":
		return c.completeFusedRelay(r, name, actor)
	// Refresh rotates credentials for an existing authorized connection.
	case "refresh":
		return c.refreshFused(r, name)
	// Handle only this explicitly supported connection action.
	case "token":
		return c.fusedRuntimeToken(r, name, actor)
	// App-bound grants publish only versions admitted by their original consent ceiling.
	case "deploy":
		return c.deployRuntimeFused(r, name)
	}
	return endpointNotFound()
}

// fusedStatuses projects only the public connection lifecycle.
func (c *Controller) fusedStatuses(r *http.Request) (any, error) {
	result := map[string]any{}
	// Never return the durable record itself, which contains credential ciphertext.
	connections, err := collectionMap[*FusedOAuthConnection](r.Context(), c.store, "fusedConnections", store.CollectionQuery{})
	if err != nil {
		return nil, err
	}
	for name, v := range connections {
		result[name] = map[string]any{"engineUrl": v.EngineURL, "status": v.Status, "expiresAt": v.ExpiresAt, "runtime": v.Runtime, "manifestDigest": v.ManifestDigest, "appFamilyId": v.AppFamilyID, "defaultBucketId": v.DefaultBucketID, "buckets": v.Buckets}
	}
	return map[string]any{"connections": result, "redirectUri": "https://" + c.state.Hostname + "/fused/oauth/callback"}, nil
}

// saveFusedPrivate encrypts credentials before changing the durable record.
func (c *Controller) saveFusedPrivate(v *FusedOAuthConnection, private fusedOAuthPrivate) error {
	data, err := json.Marshal(private)
	// Propagate failure before publishing any connection state.
	if err != nil {
		return err
	}
	ciphertext, err := c.store.Encrypt(string(data))
	// Propagate failure before publishing any connection state.
	if err != nil {
		return err
	}
	v.Ciphertext = ciphertext
	return nil
}

// loadFusedPrivate authenticates ciphertext before interpreting credentials.
func (c *Controller) loadFusedPrivate(name string) (*FusedOAuthConnection, fusedOAuthPrivate, error) {
	var private fusedOAuthPrivate
	v := c.state.FusedConnections[name]
	// An unknown connection cannot expose or consume credentials.
	if v == nil {
		return nil, private, model.Err("NOT_FOUND", "Fused connection not found")
	}
	raw, err := c.store.Decrypt(v.Ciphertext)
	// Propagate failure before publishing any connection state.
	if err != nil {
		return nil, private, err
	}
	err = json.Unmarshal([]byte(raw), &private)
	return v, private, err
}

// beginFused binds an owner-approved authorization attempt to the controller's canonical callback.
func (c *Controller) beginFused(r *http.Request, name, actor string) (any, error) {
	var input fusedConnectInput
	// Propagate failure before publishing any connection state.
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	// Reject incomplete runtime requests before registration can create an upstream client.
	runtime, err := c.validateRuntimeConsent(input)
	// Invalid runtime declarations cannot reach Engine discovery.
	if err != nil {
		return nil, err
	}
	// Require a bounded registered OAuth client identity.
	if len(input.ClientID) > 256 {
		return nil, model.Err("INVALID_REQUEST", "A registered Engine OAuth client ID is required")
	}
	// Existing connections must be explicitly disconnected before replacement.
	if c.fusedConnectionOccupied(name) {
		return nil, model.Err("CONFLICT", "Fused connection exists; disconnect it first")
	}
	metadata, err := engineoauth.Discover(r.Context(), input.EngineURL)
	// Propagate failure before publishing any connection state.
	if err != nil {
		return nil, model.Err("FUSED_OAUTH", err.Error())
	}
	// Runtime endpoints share the verified issuer but use the specialized manifest-scoped issuer.
	if runtime != nil {
		metadata.AuthorizationEndpoint = metadata.Issuer + "/oauth/runtime/authorize"
		metadata.TokenEndpoint = metadata.Issuer + "/oauth/runtime/token"
	}
	private, err := c.prepareFusedClient(r, input, metadata, actor)
	// Client registration must succeed before creating a pending attempt.
	if err != nil {
		return nil, err
	}
	private.Runtime = runtime

	state := random() + random()
	private.StateHash = verifier(state)
	pending := &FusedOAuthConnection{EngineURL: metadata.Issuer, Status: "pending", ExpiresAt: c.opts.Now().Add(10 * time.Minute)}
	// Propagate failure before publishing any connection state.
	if err = c.saveFusedPrivate(pending, private); err != nil {
		return nil, err
	}
	// Initialize the collection for controllers created before OAuth support.
	if c.state.FusedConnections == nil {
		c.state.FusedConnections = map[string]*FusedOAuthConnection{}
	}
	c.state.FusedConnections[name] = pending
	digest := sha256.Sum256([]byte(private.Verifier))
	query := url.Values{"client_id": {private.ClientID}, "response_type": {"code"}, "redirect_uri": {private.RedirectURI}, "state": {state}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "scope": {"app.sdk.tokens.manage app.api.tokens.manage"}}
	// Registration chooses execution-only or app-bound deployment authority explicitly.
	query.Set("scope", fusedConsentScope(runtime))
	return map[string]any{"name": name, "authorizationUrl": metadata.AuthorizationEndpoint + "?" + query.Encode(), "redirectUri": private.RedirectURI, "expiresAt": pending.ExpiresAt, "clientExpiresAt": private.ClientExpiresAt}, nil
}

// fusedTokenForm keeps client authentication consistent across redemption, refresh and revocation.
func fusedTokenForm(private fusedOAuthPrivate) url.Values {
	return url.Values{"client_id": {private.ClientID}, "client_secret": {private.ClientSecret}}
}

// refreshFused rotates the encrypted OAuth credential before any later operation can use it.
func (c *Controller) refreshFused(r *http.Request, name string) (any, error) {
	// Propagate failure before publishing any connection state.
	if err := emptyBody(r); err != nil {
		return nil, err
	}
	v, private, err := c.loadFusedPrivate(name)
	// Propagate failure before publishing any connection state.
	if err != nil {
		return nil, err
	}
	// Only the expected live connection state may advance.
	if private.Runtime != nil && !private.Runtime.Management {
		return nil, model.Err("FUSED_OAUTH", "Runtime credentials do not use management OAuth refresh")
	}
	// Only a retained management refresh token can rotate this connection.
	if v.Status != "connected" || private.Tokens.RefreshToken == "" {
		return nil, model.Err("FUSED_OAUTH", "Reconnect this Engine to authorize access")
	}
	// Propagate failure before publishing any connection state.
	if err = c.rotateFused(r, v, &private); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "status": v.Status, "expiresAt": v.ExpiresAt}, nil
}

// rotateFused saves rotated refresh tokens immediately, even if subsequent deployment preparation fails.
func (c *Controller) rotateFused(r *http.Request, v *FusedOAuthConnection, private *fusedOAuthPrivate) error {
	form := fusedTokenForm(*private)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", private.Tokens.RefreshToken)
	tokens, err := engineoauth.Exchange(r.Context(), private.Metadata.TokenEndpoint, form)
	// Propagate failure before publishing any connection state.
	if err != nil {
		return model.Err("FUSED_OAUTH", err.Error()+"; reconnect if authorization expired or was revoked")
	}
	// Keep the current refresh token if Engine does not rotate it.
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = private.Tokens.RefreshToken
	}
	// A refresh cannot broaden an app-specific grant into generic management authority.
	if private.Runtime != nil && private.Runtime.Management && !validDeploymentTokens(tokens) {
		return model.Err("FUSED_OAUTH", "Invalid app-deployment refresh response")
	}
	private.Tokens = tokens
	// Propagate failure before publishing any connection state.
	if err = c.saveFusedPrivate(v, *private); err != nil {
		return err
	}
	v.ExpiresAt = c.opts.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	return c.store.Save(c.state)
}

// disconnectFused revokes the management grant before forgetting local credentials.
func (c *Controller) disconnectFused(r *http.Request, name string) (any, error) {
	// Propagate failure before publishing any connection state.
	if err := emptyBody(r); err != nil {
		return nil, err
	}
	v, private, err := c.loadFusedPrivate(name)
	// Propagate failure before publishing any connection state.
	if err != nil {
		return nil, err
	}
	// Pending attempts have no issued grant to revoke.
	if v.Status == "connected" && v.Runtime != nil {
		// Runtime consent grants are revoked through their dedicated client, without management authority.
		if err := c.revokeRuntimeFused(r, v, private); err != nil {
			return nil, err
		}
		delete(c.state.FusedConnections, name)
		return map[string]any{"disconnected": true, "runtimeTokensRevoked": true}, nil
	}
	// Management connections retain the existing refresh-token revocation flow.
	if v.Status == "connected" {
		form := fusedTokenForm(private)
		token := private.Tokens.RefreshToken
		// A grant without a refresh token is revoked through its access token.
		if token == "" {
			token = private.Tokens.AccessToken
		}
		form.Set("token", token)
		// Propagate failure before publishing any connection state.
		if err = engineoauth.Request(r.Context(), "POST", private.Metadata.RevocationEndpoint, "", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), nil); err != nil {
			return nil, model.Err("FUSED_OAUTH", err.Error())
		}
	}
	delete(c.state.FusedConnections, name)
	return map[string]any{"disconnected": true, "runtimeTokensRevoked": false}, nil
}

// fusedClientSecret resolves an optional confidential-client secret without putting it in a request body.
func (c *Controller) fusedClientSecret(ref string) (string, error) {
	// Public clients authenticate code redemption with PKCE alone.
	if ref == "" {
		return "", nil
	}
	secret := c.state.Secrets[ref]
	// Missing references must not silently downgrade a confidential client.
	if secret == nil {
		return "", model.Err("NOT_FOUND", "OAuth client secret reference not found")
	}
	return c.store.Decrypt(secret.Revisions[secret.Current])
}

// fusedConnectionOccupied prevents replacing a live grant or unfinished ceremony.
func (c *Controller) fusedConnectionOccupied(name string) bool {
	prior := c.state.FusedConnections[name]
	return prior != nil && (prior.Status == "connected" || prior.ExpiresAt.After(c.opts.Now()))
}
