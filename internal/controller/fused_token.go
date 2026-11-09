package controller

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"hostlier/internal/engineoauth"
	"hostlier/internal/model"
)

var fusedSecretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)

var fusedUUID = regexp.MustCompile(`^[a-fA-F0-9]{8}-(?:[a-fA-F0-9]{4}-){3}[a-fA-F0-9]{12}$`)

// fusedRuntimeToken creates one named family credential directly into an encrypted secret revision.
func (c *Controller) fusedRuntimeToken(r *http.Request, name, actor string) (any, error) {
	var input fusedRuntimeTokenInput
	// Propagate failure before publishing any connection state.
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	// Explicit family, secret and operation policy prevent accidental unbounded provisioning.
	if !input.valid() {
		return nil, model.Err("INVALID_REQUEST", "Specify family UUID, secret name, unique token name, and allowed operations")
	}
	// Provisioning never silently replaces a credential already used by another deployment.
	if c.state.Secrets[input.SecretName] != nil {
		return nil, model.Err("CONFLICT", "Secret already exists; choose a new runtime-token secret")
	}
	v, private, err := c.usableFused(r, name)
	// Propagate failure before publishing any connection state.
	if err != nil {
		return nil, err
	}

	body, _ := json.Marshal(map[string]any{"name": input.TokenName, "allow": input.Allow})
	var response struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	endpoint := v.EngineURL + "/workspace/app-tokens?app_family_id=" + url.QueryEscape(input.FamilyID)
	// Propagate failure before publishing any connection state.
	if err = engineoauth.Request(r.Context(), "POST", endpoint, private.Tokens.AccessToken, "application/json", strings.NewReader(string(body)), &response); err != nil {
		return nil, model.Err("FUSED_OAUTH", err.Error()+"; inspect Engine token list before retrying")
	}
	// Reject invalid protocol state before advancing the connection.
	if response.Token == "" {
		return nil, model.Err("FUSED_OAUTH", "Engine did not return a runtime token")
	}
	ciphertext, err := c.store.Encrypt(response.Token)
	// Propagate failure before publishing any connection state.
	if err != nil {
		return nil, err
	}
	revision := random()
	c.state.Secrets[input.SecretName] = &Secret{Current: revision, Revisions: map[string]string{revision: ciphertext}}
	c.state.Generation++
	c.log("Fused runtime-token secret provisioned", "", "info")
	return map[string]any{"name": input.SecretName, "revision": revision, "familyId": input.FamilyID, "tokenId": response.ID, "tokenName": input.TokenName, "engineUrl": v.EngineURL}, nil
}

// fusedRuntimeTokenInput requires an explicit family, secret and operation policy.
type fusedRuntimeTokenInput struct {
	FamilyID   string   `json:"familyId"`
	SecretName string   `json:"secretName"`
	TokenName  string   `json:"tokenName"`
	Allow      []string `json:"allow"`
}

// valid refuses ambiguous token ownership before contacting Engine.
func (input fusedRuntimeTokenInput) valid() bool {
	return fusedUUID.MatchString(input.FamilyID) && fusedSecretName.MatchString(input.SecretName) && scopeName.MatchString(input.TokenName) && len(input.Allow) > 0
}

// usableFused refreshes near-expiry credentials without releasing them to callers outside the controller.
func (c *Controller) usableFused(r *http.Request, name string) (*FusedOAuthConnection, fusedOAuthPrivate, error) {
	v, private, err := c.loadFusedPrivate(name)
	// A missing connection cannot provision a runtime credential.
	if err != nil {
		return nil, private, err
	}
	// Only the expected live connection state may advance.
	if v.Status != "connected" || private.Runtime != nil {
		return nil, private, model.Err("FUSED_OAUTH", "Connect to Engine before provisioning a Minion token")
	}
	// Never provision with an expired or nearly expired access token.
	if !v.ExpiresAt.After(c.opts.Now().Add(time.Minute)) {
		err = c.rotateFused(r, v, &private)
	}
	return v, private, err
}
