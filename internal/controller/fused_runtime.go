package controller

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/engineoauth"
	"hostlier/internal/model"
)

// fusedRuntimeConsent retains only public requirements beside the private OAuth ceremony.
type fusedRuntimeConsent struct {
	Management     bool
	ManifestDigest string
	GRPCURL        string
	SecretName     string
}

// fusedConsentScope keeps runtime credentials outside management permission namespaces.
func fusedConsentScope(runtime *fusedRuntimeConsent) string {
	// Management is limited by Engine to the manifest-approved app family.
	if runtime != nil && runtime.Management {
		return "app.api.deploy"
	}
	// Existing runtime-only grants retain their execution-only audience.
	if runtime != nil {
		return "api.execute"
	}
	return "app.sdk.tokens.manage app.api.tokens.manage"
}

// validateRuntimeConsent reuses deployment validation and reserves a new secret identity.
func (c *Controller) validateRuntimeConsent(input fusedConnectInput) (*fusedRuntimeConsent, error) {
	// Reject partial or mixed requests before any upstream registration.
	if err := validateRuntimeConsentShape(input); err != nil {
		return nil, err
	}
	// Existing management requests carry no runtime declaration.
	if input.Manifest == "" {
		return nil, nil
	}
	connection := model.FusedConnection{GRPCURL: input.GRPCURL, AppID: "00000000-0000-4000-8000-000000000001", TokenSecret: input.SecretName}
	// Deployment and consent must agree on transport and secret-reference syntax.
	if err := config.ValidateFusedConnection(connection); err != nil {
		return nil, err
	}
	// A secret belongs to one pending or completed provisioning operation.
	if err := c.runtimeSecretAvailable(input.SecretName); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(input.Manifest))
	return &fusedRuntimeConsent{Management: input.Management, ManifestDigest: "sha256:" + hex.EncodeToString(digest[:]), GRPCURL: input.GRPCURL, SecretName: input.SecretName}, nil
}

// validateRuntimeConsentShape distinguishes management requests from manifest-bound runtime registration.
func validateRuntimeConsentShape(input fusedConnectInput) error {
	// Ordinary connections cannot smuggle partial runtime parameters.
	if input.Manifest == "" {
		// These fields only make sense alongside compiler output.
		if input.GRPCURL != "" || input.SecretName != "" || input.Management {
			return model.Err("INVALID_REQUEST", "Runtime consent requires a compiled manifest")
		}
		return nil
	}
	// Runtime clients must be created for this exact bounded manifest.
	if input.ClientID != "" || input.ClientSecretRef != "" || len(input.Manifest) > 65536 || !json.Valid([]byte(input.Manifest)) {
		return model.Err("INVALID_REQUEST", "Use a compiled runtime manifest and dynamic client registration")
	}
	return nil
}

// runtimeSecretAvailable avoids replacement and prevents concurrent ceremonies claiming one secret name.
func (c *Controller) runtimeSecretAvailable(secret string) error {
	// Existing credentials are never rotated as an incidental consent side effect.
	if c.state.Secrets[secret] != nil {
		return model.Err("CONFLICT", "Runtime-token secret already exists")
	}
	// Pending registrations reserve names until their browser window expires.
	for name, connection := range c.state.FusedConnections {
		// Expired or completed ceremonies have no pending reservation.
		if connection.Status != "pending" || !connection.ExpiresAt.After(c.opts.Now()) {
			continue
		}
		_, private, err := c.loadFusedPrivate(name)
		// Corrupt state cannot release another operation's reservation.
		if err != nil {
			return err
		}
		// A matching reservation requires a different name or completion of the existing ceremony.
		if private.Runtime != nil && private.Runtime.SecretName == secret {
			return model.Err("CONFLICT", "Runtime-token secret is reserved by pending consent")
		}
	}
	return nil
}

// exchangeRuntimeFused redeems the consumed code once and never exposes execution tokens to the CLI.
func (c *Controller) exchangeRuntimeFused(r *http.Request, name string, private fusedOAuthPrivate, form url.Values) (any, error) {
	var response fusedRuntimeResult
	err := engineoauth.Request(r.Context(), "POST", private.Metadata.TokenEndpoint, "", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), &response)
	// An ambiguous exchange must not be repeated automatically.
	if err != nil {
		return nil, model.Err("FUSED_OAUTH", err.Error()+"; review Fused Connected Apps before retrying")
	}
	// Validate identity, exact manifest binding and token syntax before storing a deployment credential.
	if !response.valid(private.Runtime.ManifestDigest) || private.Runtime.Management && !validDeploymentTokens(response.Tokens) {
		return nil, model.Err("FUSED_OAUTH", "Invalid runtime consent response; review Fused Connected Apps")
	}

	connection := model.FusedConnection{GRPCURL: private.Runtime.GRPCURL, AppID: response.AppID, TokenSecret: private.Runtime.SecretName}
	// Reuse deployment validation rather than trusting returned identifiers implicitly.
	if err := config.ValidateFusedConnection(connection); err != nil {
		return nil, err
	}
	// Publish the secret and immutable binding in one durable controller update.
	if err := c.completeRuntimeFused(name, private, connection, response); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "status": "connected", "runtime": connection, "manifestDigest": response.ManifestDigest, "appFamilyId": response.AppFamilyID, "defaultBucketId": response.DefaultBucketID, "buckets": response.Buckets}, nil
}

// completeRuntimeFused atomically publishes the encrypted token and its public immutable app binding.
func (c *Controller) completeRuntimeFused(name string, private fusedOAuthPrivate, connection model.FusedConnection, response fusedRuntimeResult) error {
	prior, stored, err := c.loadFusedPrivate(name)
	// Cancellation or state replacement must never resurrect an old ceremony.
	if err != nil {
		return err
	}
	// Only the original consumed ceremony can publish this credential.
	if prior.Status != "exchanging" || stored.Verifier == "" || subtle.ConstantTimeCompare([]byte(stored.Verifier), []byte(private.Verifier)) != 1 {
		return model.Err("FUSED_OAUTH", "Connection was cancelled")
	}
	// A secret created while consent was pending must remain untouched.
	if c.state.Secrets[connection.TokenSecret] != nil {
		return model.Err("CONFLICT", "Runtime-token secret already exists")
	}
	encrypted, err := c.store.Encrypt(response.RuntimeToken)
	// Encryption failure must precede every state mutation.
	if err != nil {
		return err
	}
	next := *prior
	next.Status, next.ExpiresAt = "connected", time.Time{}
	// Only controller-owned deployment grants retain OAuth tokens; the workload gets frt_ only.
	if private.Runtime.Management {
		private.Tokens = response.Tokens
		next.ExpiresAt = c.opts.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	}
	next.Runtime, next.AppFamilyID, next.ManifestDigest = &connection, response.AppFamilyID, private.Runtime.ManifestDigest
	next.DefaultBucketID, next.Buckets = response.DefaultBucketID, response.Buckets
	private.Verifier, private.StateHash = "", ""
	// Keep the client credentials encrypted for explicit revocation; keep app-bound deployment tokens only inside this encrypted state.
	if err := c.saveFusedPrivate(&next, private); err != nil {
		return err
	}
	revision := random()
	generation := c.state.Generation
	c.state.Secrets[connection.TokenSecret] = &Secret{Current: revision, Revisions: map[string]string{revision: encrypted}}
	c.state.FusedConnections[name] = &next
	c.state.Generation++
	// Failed durable publication restores memory so subsequent requests cannot use an unsaved credential.
	if err := c.store.Save(c.state); err != nil {
		delete(c.state.Secrets, connection.TokenSecret)
		c.state.FusedConnections[name], c.state.Generation = prior, generation
		return err
	}
	return nil
}

// revokeRuntimeFused revokes only this consent's dedicated runtime token using its own client identity.
func (c *Controller) revokeRuntimeFused(r *http.Request, connection *FusedOAuthConnection, private fusedOAuthPrivate) error {
	secret := c.state.Secrets[connection.Runtime.TokenSecret]
	// Missing local material must not be reported as successful upstream revocation.
	if secret == nil {
		return model.Err("FUSED_OAUTH", "Runtime secret is missing; revoke the app in Fused Connected Apps")
	}
	token, err := c.store.Decrypt(secret.Revisions[secret.Current])
	// Decryption failure cannot downgrade this operation to local-only disconnection.
	if err != nil {
		return err
	}
	form := fusedTokenForm(private)
	// Deployment disconnection revokes the whole app-bound grant, including its issued runtime credentials.
	if private.Runtime != nil && private.Runtime.Management {
		token = private.Tokens.RefreshToken
	}
	form.Set("token", token)
	return engineoauth.Request(r.Context(), "POST", private.Metadata.Issuer+"/oauth/runtime/revoke", "", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), nil)
}

// fusedRuntimeResult is the version-bound execution credential returned once by Engine.
type fusedRuntimeResult struct {
	engineoauth.Tokens
	AppID           string              `json:"app_id"`
	AppFamilyID     string              `json:"app_family_id"`
	RuntimeToken    string              `json:"runtime_token"`
	ManifestDigest  string              `json:"manifest_digest"`
	DefaultBucketID string              `json:"default_bucket_id"`
	Buckets         []model.FusedBucket `json:"buckets"`
}

// valid checks both exact compiler binding and bounded public identity before accepting secret material.
func (response fusedRuntimeResult) valid(digest string) bool {
	return fusedUUID.MatchString(response.AppID) && fusedUUID.MatchString(response.AppFamilyID) && response.ManifestDigest == digest && strings.HasPrefix(response.RuntimeToken, "frt_") && len(response.RuntimeToken) > 4 && len(response.RuntimeToken) <= 8192 && !strings.ContainsAny(response.RuntimeToken, "\r\n\t ") && response.validBuckets()
}

// validBuckets requires a finite, unique scope containing the approved default bucket.
func (response fusedRuntimeResult) validBuckets() bool {
	// Missing or unbounded scopes cannot be rendered as a successful consent result.
	if len(response.Buckets) == 0 || len(response.Buckets) > 32 {
		return false
	}
	seen := map[string]bool{}
	// Names are display metadata; UUIDs are the actual request-routing identities.
	for _, bucket := range response.Buckets {
		// Duplicates and malformed identifiers must not create ambiguous choices.
		if !fusedUUID.MatchString(bucket.ID) || seen[bucket.ID] || bucket.Name == "" {
			return false
		}
		seen[bucket.ID] = true
	}
	return seen[response.DefaultBucketID]
}
