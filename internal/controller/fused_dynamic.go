package controller

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hostlier/internal/engineoauth"
	"hostlier/internal/model"
)

// fusedConnectInput selects dynamic loopback registration or an explicitly registered HTTPS client.
type fusedConnectInput struct {
	EngineURL       string `json:"engineUrl"`
	ClientID        string `json:"clientId,omitempty"`
	ClientSecretRef string `json:"clientSecretRef,omitempty"`
	RedirectURI     string `json:"redirectUri,omitempty"`
	Manifest        string `json:"manifest,omitempty"`
	Management      bool   `json:"management,omitempty"`
	GRPCURL         string `json:"grpcUrl,omitempty"`
	SecretName      string `json:"secretName,omitempty"`
}

// prepareFusedClient keeps registration secrets on the controller while the CLI receives only the code.
func (c *Controller) prepareFusedClient(r *http.Request, input fusedConnectInput, metadata engineoauth.Metadata, actor string) (fusedOAuthPrivate, error) {
	private := fusedOAuthPrivate{Metadata: metadata, ClientID: input.ClientID, RedirectURI: "https://" + c.state.Hostname + "/fused/oauth/callback", Verifier: random() + random()}
	// Explicit registration supports an existing permanent client without changing dynamic-client policy.
	if input.ClientID != "" {
		secret, err := c.fusedClientSecret(input.ClientSecretRef)
		private.ClientSecret = secret
		return private, err
	}
	// Dynamic clients must deliver their code to the initiating CLI's literal loopback listener.
	if !fusedLoopbackCallback(input.RedirectURI) {
		return private, model.Err("INVALID_REQUEST", "Dynamic OAuth requires the CLI loopback callback")
	}
	registrationPath := "/oauth/register"
	registrationBody := map[string]any{"redirect_uri": input.RedirectURI, "scopes": []string{"app.sdk.tokens.manage", "app.api.tokens.manage"}, "name": "Hostlier controller"}
	// Runtime consent registers the compiler's exact declaration with optional app-family deployment authority.
	if input.Manifest != "" {
		registrationPath = "/oauth/runtime/register"
		registrationBody = map[string]any{"redirect_uri": input.RedirectURI, "manifest": input.Manifest, "name": "Hostlier Minion"}
		// Older execution-only registrations retain their original request shape.
		if input.Management {
			registrationBody["management"] = true
		}
	}
	body, _ := json.Marshal(registrationBody)
	var registration struct {
		ClientID     string    `json:"client_id"`
		ClientSecret string    `json:"client_secret"`
		ExpiresAt    time.Time `json:"client_id_expires_at"`
	}
	// Fused's documented local-client registration endpoint is bound to the discovered issuer.
	err := engineoauth.Request(r.Context(), "POST", metadata.Issuer+registrationPath, "", "application/json", strings.NewReader(string(body)), &registration)
	// Failed registration cannot produce usable client credentials.
	if err != nil {
		return private, model.Err("FUSED_OAUTH", err.Error())
	}
	// Require complete credentials with a live registration lease.
	if registration.ClientID == "" || registration.ClientSecret == "" || !registration.ExpiresAt.After(c.opts.Now()) {
		return private, model.Err("FUSED_OAUTH", "Invalid dynamic client registration")
	}
	private.ClientID, private.ClientSecret = registration.ClientID, registration.ClientSecret
	private.ClientExpiresAt = registration.ExpiresAt
	private.RedirectURI, private.Actor = input.RedirectURI, actor
	return private, nil
}

// fusedLoopbackCallback refuses callback aliases, credentials and alternate paths.
func fusedLoopbackCallback(raw string) bool {
	u, err := url.Parse(raw)
	// Require the exact callback shape implemented by the Hostlier CLI.
	if err != nil {
		return false
	}
	return u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.User == nil && u.Path == "/fused/callback" && u.RawQuery == "" && u.Fragment == ""
}

// completeFusedRelay binds CLI code relay to the original authenticated owner and PKCE state.
func (c *Controller) completeFusedRelay(r *http.Request, name, actor string) (any, error) {
	var input struct {
		State string `json:"state"`
		Code  string `json:"code"`
		Error string `json:"error,omitempty"`
	}
	// Strict decoding keeps arbitrary callback parameters out of the token request.
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	_, private, err := c.loadFusedPrivate(name)
	// Relay requires the original authenticated private attempt.
	if err != nil {
		return nil, err
	}
	// An enrolled device cannot finish another owner's connection using a captured code.
	if private.Actor == "" || private.Actor != actor || subtle.ConstantTimeCompare([]byte(private.StateHash), []byte(verifier(input.State))) != 1 {
		return nil, model.Err("UNAUTHORIZED", "OAuth attempt belongs to another initiator")
	}
	consumed, private, err := c.consumeFusedAttemptLocked(input.State, true)
	// Consume only the named, unexpired attempt before exchanging its code.
	if err != nil || consumed != name {
		return nil, model.Err("FUSED_OAUTH", "Invalid or expired OAuth attempt")
	}
	// A declined authorization cannot advance to token exchange.
	if input.Error != "" || input.Code == "" {
		return nil, model.Err("FUSED_OAUTH", "Engine authorization declined")
	}
	return c.exchangeFusedRelay(r, name, input.Code, private)
}

// exchangeFusedRelay redeems the consumed PKCE attempt and persists its private tokens.
func (c *Controller) exchangeFusedRelay(r *http.Request, name, code string, private fusedOAuthPrivate) (any, error) {
	form := fusedTokenForm(private)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", private.RedirectURI)
	form.Set("code_verifier", private.Verifier)
	// Runtime grants deliver an independent execution token directly into the secret store.
	if private.Runtime != nil {
		return c.exchangeRuntimeFused(r, name, private, form)
	}
	tokens, err := engineoauth.Exchange(r.Context(), private.Metadata.TokenEndpoint, form)
	// Exchange failure leaves the consumed attempt unavailable for replay.
	if err != nil {
		return nil, model.Err("FUSED_OAUTH", err.Error())
	}
	private.Tokens = tokens
	// Report success only after the token state is durably published.
	if err = c.completeFusedAttemptLocked(name, private); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "status": "connected", "clientExpiresAt": private.ClientExpiresAt}, nil
}
