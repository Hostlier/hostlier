package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/engineoauth"
	"hostlier/internal/model"
)

// validDeploymentTokens accepts only the specialized app-bound OAuth authority, never generic management scopes.
func validDeploymentTokens(tokens engineoauth.Tokens) bool {
	return tokens.Scope == "app.api.deploy" && strings.EqualFold(tokens.TokenType, "Bearer") && tokens.ExpiresIn > 0 && tokens.ExpiresIn <= 86400 && tokens.AccessToken != "" && tokens.RefreshToken != "" && len(tokens.AccessToken) <= 8192 && len(tokens.RefreshToken) <= 8192 && !strings.ContainsAny(tokens.AccessToken+tokens.RefreshToken, "\r\n\t ") && !strings.HasPrefix(tokens.AccessToken, "frt_")
}

// runtimeDeploymentInput binds an update to the current Engine and a new rollback-safe secret name.
type runtimeDeploymentInput struct {
	EngineURL  string `json:"engineUrl"`
	GRPCURL    string `json:"grpcUrl"`
	SecretName string `json:"secretName"`
	Manifest   string `json:"manifest"`
}

// deployRuntimeFused publishes a version through an existing family-bound grant without repeating consent.
func (c *Controller) deployRuntimeFused(r *http.Request, name string) (any, error) {
	var input runtimeDeploymentInput
	// Strict request decoding prevents this narrow route accepting arbitrary app IDs or bucket changes.
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	prior, private, err := c.loadFusedPrivate(name)
	// Only a connected app-deployment grant can create later versions.
	if err != nil {
		return nil, err
	}
	if prior.Status != "connected" || prior.Runtime == nil || private.Runtime == nil || !private.Runtime.Management || !validDeploymentTokens(private.Tokens) {
		return nil, model.Err("FUSED_OAUTH", "Authorize this Minion with app-specific deployment consent first")
	}
	// An app update cannot redirect controller credentials to a different Engine or transport.
	if input.EngineURL != prior.EngineURL || input.GRPCURL != prior.Runtime.GRPCURL || len(input.Manifest) == 0 || len(input.Manifest) > 65536 || !json.Valid([]byte(input.Manifest)) {
		return nil, model.Err("INVALID_REQUEST", "Use a compiled manifest for this connection's Engine and gRPC endpoint")
	}
	digestBytes := sha256.Sum256([]byte(input.Manifest))
	digest := "sha256:" + hex.EncodeToString(digestBytes[:])
	// A repeated completed request recovers its public binding without issuing another credential.
	if prior.ManifestDigest == digest && prior.Runtime.TokenSecret == input.SecretName {
		return runtimeDeploymentStatus(name, prior), nil
	}
	connection := model.FusedConnection{GRPCURL: input.GRPCURL, AppID: prior.Runtime.AppID, TokenSecret: input.SecretName}
	// A fresh secret name preserves every earlier release's captured rollback credentials.
	if err := config.ValidateFusedConnection(connection); err != nil {
		return nil, err
	}
	if err := c.runtimeSecretAvailable(input.SecretName); err != nil {
		return nil, err
	}
	// Rotate expired management authority before publishing a version, retaining it only on the controller.
	if !prior.ExpiresAt.After(c.opts.Now().Add(time.Minute)) {
		if err := c.rotateFused(r, prior, &private); err != nil {
			return nil, err
		}
	}
	body, _ := json.Marshal(map[string]string{"manifest": input.Manifest})
	var response fusedRuntimeResult
	// Engine enforces the family, operations, provider versions and approved bucket ceiling on every update.
	if err := engineoauth.Request(r.Context(), "POST", private.Metadata.Issuer+"/oauth/runtime/deploy", private.Tokens.AccessToken, "application/json", strings.NewReader(string(body)), &response); err != nil {
		return nil, model.Err("FUSED_OAUTH", err.Error()+"; inspect the Fused app before retrying an uncertain deployment; broader access needs new consent")
	}
	// A successful upstream response must still prove this exact app and approved routing scope.
	if !response.valid(digest) || response.AppFamilyID != prior.AppFamilyID || !sameRuntimeBuckets(prior, response) {
		return nil, model.Err("FUSED_OAUTH", "Deployment response does not match this app's consent")
	}
	connection.AppID = response.AppID
	// Save both the new credential and public version binding as one durable controller transaction.
	if err := c.completeRuntimeDeployment(name, prior, private, connection, response); err != nil {
		return nil, err
	}
	return runtimeDeploymentStatus(name, c.state.FusedConnections[name]), nil
}

// sameRuntimeBuckets prevents a response from silently broadening or changing approved credential routing.
func sameRuntimeBuckets(prior *FusedOAuthConnection, response fusedRuntimeResult) bool {
	if prior.DefaultBucketID != response.DefaultBucketID || len(prior.Buckets) != len(response.Buckets) {
		return false
	}
	ids := make(map[string]bool, len(prior.Buckets))
	for _, bucket := range prior.Buckets {
		ids[bucket.ID] = true
	}
	for _, bucket := range response.Buckets {
		// Display-name changes are harmless, but only approved immutable bucket identities may be returned.
		if !ids[bucket.ID] {
			return false
		}
	}
	return true
}

// completeRuntimeDeployment preserves previous secrets and replaces only this connection's current version pointer.
func (c *Controller) completeRuntimeDeployment(name string, prior *FusedOAuthConnection, private fusedOAuthPrivate, connection model.FusedConnection, response fusedRuntimeResult) error {
	encrypted, err := c.store.Encrypt(response.RuntimeToken)
	// Prepare every encrypted byte before modifying shared controller state.
	if err != nil {
		return err
	}
	next := *prior
	next.Runtime, next.ManifestDigest = &connection, response.ManifestDigest
	next.DefaultBucketID, next.Buckets = response.DefaultBucketID, response.Buckets
	private.Runtime.ManifestDigest, private.Runtime.SecretName = response.ManifestDigest, connection.TokenSecret
	if err := c.saveFusedPrivate(&next, private); err != nil {
		return err
	}
	revision, generation := random(), c.state.Generation
	c.state.Secrets[connection.TokenSecret] = &Secret{Current: revision, Revisions: map[string]string{revision: encrypted}}
	c.state.FusedConnections[name] = &next
	c.state.Generation++
	// Disk failure must not expose an update that cannot survive controller restart.
	if err := c.store.Save(c.state); err != nil {
		delete(c.state.Secrets, connection.TokenSecret)
		c.state.FusedConnections[name], c.state.Generation = prior, generation
		return err
	}
	return nil
}

// runtimeDeploymentStatus excludes both management tokens and workload execution secrets from every public result.
func runtimeDeploymentStatus(name string, connection *FusedOAuthConnection) map[string]any {
	return map[string]any{"name": name, "status": connection.Status, "runtime": connection.Runtime, "manifestDigest": connection.ManifestDigest, "appFamilyId": connection.AppFamilyID, "defaultBucketId": connection.DefaultBucketID, "buckets": connection.Buckets}
}
