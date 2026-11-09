package controller

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"hostlier/internal/model"
	"hostlier/internal/store"
)

var secretOwnerName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// apiSecrets routes authenticated requests for encrypted secret revision management.
func (c *Controller) apiSecrets(r *http.Request, p []string, actor string) (any, error) {
	method := r.Method

	// Return names and current revision identities without encrypted payloads.
	if method == "GET" && len(p) == 2 {
		values := []any{}
		items, err := c.store.Collection(r.Context(), "secrets", store.CollectionQuery{})
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			var secret Secret
			if err := json.Unmarshal(item.Payload, &secret); err != nil {
				return nil, err
			}
			values = append(values, map[string]string{"name": item.Key, "revision": secret.Current})
		}
		return map[string]any{"secrets": values}, nil
	}
	// Reject paths that do not name exactly one secret.
	if len(p) != 3 {
		return endpointNotFound()
	}
	name := p[2]
	// Reject this path: Invalid secret name.
	if strings.ContainsAny(name, "/\\") || required(name) != nil {
		return nil, model.Err("INVALID_SECRET", "Invalid secret name")
	}
	// Route uploads through encrypted, idempotent revision creation.
	if method == "PUT" {
		return c.putSecret(r, name, actor)
	}
	// Validate retained references before secret deletion.
	if method == "DELETE" {
		return c.deleteSecret(r, name)
	}

	return endpointNotFound()
}

// putSecret encrypts secret revisions and preserves durable idempotent upload replay.
func (c *Controller) putSecret(r *http.Request, name, actor string) (any, error) {
	var b secretWriteRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	// Reject this path: Secret must contain 1–65536 bytes.
	if len(b.Value) == 0 || len(b.Value) > 65536 {
		return nil, model.Err("INVALID_SECRET", "Secret must contain 1–65536 bytes")
	}
	if b.Owner != "" && (!secretOwnerName.MatchString(b.Owner) || strings.HasSuffix(b.Owner, "-")) {
		return nil, model.Err("INVALID_SECRET", "Invalid owning application name")
	}
	if existing := c.state.Secrets[name]; existing != nil && existing.Owner != b.Owner {
		return nil, model.Err("SECRET_OWNER", "Secret ownership cannot be changed")
	}
	// Require a nonempty bounded identity before continuing.
	if err := required(b.RequestID); err != nil {
		return nil, err
	}
	key := verifier(actor + "|" + b.RequestID)
	signature := c.store.Authenticate(name + "|" + b.Value)
	// Replay an existing upload only when its keyed request signature matches.
	if prior, ok := c.state.Writes[key]; ok {
		// Reject this path: Request ID already used.
		if prior.Signature != signature {
			return nil, model.Err("IDEMPOTENCY_CONFLICT", "Request ID already used")
		}
		return map[string]string{"name": prior.Name, "revision": prior.Revision}, nil
	}
	revision, err := c.unchangedSecretRevision(name, b.Value)
	// An unreadable current revision cannot safely be treated as a new value.
	if err != nil {
		return nil, err
	}
	// Repeated declarative deployments reuse the current encrypted revision.
	if revision != "" {
		c.state.Writes[key] = Write{signature, name, revision}
		return map[string]string{"name": name, "revision": revision}, nil
	}
	ciphertext, err := c.store.Encrypt(b.Value)
	// Never persist private data when encryption fails.
	if err != nil {
		return nil, err
	}
	revision = random()
	c.retainSecretRevision(name, revision, ciphertext)
	c.state.Secrets[name].Owner = b.Owner
	c.state.Writes[key] = Write{signature, name, revision}
	c.state.Generation++
	c.log("Secret revision stored", "", "info")
	return map[string]string{"name": name, "revision": revision}, nil
}

// deleteSecret retains secrets referenced by rollback history or active operations.
func (c *Controller) deleteSecret(r *http.Request, name string) (any, error) {
	// Preserve package credentials required by accepted or retained workflow resources.
	if err := c.workflowSecretReference(name); err != nil {
		return nil, err
	}
	// Reject unexpected payload fields on this bodyless action.
	if err := emptyBody(r); err != nil {
		return nil, err
	}
	// Keep secret revisions required by retained rollback history.
	if err := c.retainedSecretReference(name); err != nil {
		return nil, err
	}
	// Keep secret revisions required by accepted operations.
	if err := c.activeSecretReference(name); err != nil {
		return nil, err
	}
	c.deleteSecretState(name)
	c.state.Generation++
	return map[string]any{"deleted": true}, nil
}

// deleteSecretState removes encrypted revisions and stale upload receipts.
// Callers perform reference and ownership checks before invoking it.
func (c *Controller) deleteSecretState(name string) {
	delete(c.state.Secrets, name)
	for id, write := range c.state.Writes {
		if write.Name == name {
			delete(c.state.Writes, id)
		}
	}
}

// retainedSecretReference protects secret revisions needed by retained deployments.
func (c *Controller) retainedSecretReference(name string) error {
	// Include every retained application when enforcing the shared state invariant.
	for _, a := range c.state.Applications {
		// The active revision may not be duplicated in rollback history.
		for _, ref := range a.SecretRefs {
			if ref.Name == name {
				return model.Err("SECRET_IN_USE", "Secret is referenced by active deployment")
			}
		}
		// Inspect retained revisions because rollback still depends on their immutable inputs.
		for _, revision := range a.Revisions {
			// Check every retained secret revision so older credentials remain protected.
			for _, ref := range revision.SecretRefs {
				// Reject this path: Secret is referenced by retained deployment.
				if ref.Name == name {
					return model.Err("SECRET_IN_USE", "Secret is referenced by retained deployment")
				}
			}
		}
	}

	return nil
}

// activeSecretReference protects secret revisions needed by accepted unfinished operations.
func (c *Controller) activeSecretReference(name string) error {
	// Inspect accepted operations as well as active state before allowing conflicting work.
	for _, op := range c.state.Operations {
		// Protect references held by accepted work that has not finished.
		if op.Status == "queued" || op.Status == "running" {
			// Check every retained secret revision so older credentials remain protected.
			for _, ref := range op.SecretRefs {
				// Reject this path: Secret referenced by active operation.
				if ref.Name == name {
					return model.Err("SECRET_IN_USE", "Secret referenced by active operation")
				}
			}
		}
	}

	return nil
}

// secretWriteRequest bounds the recognized JSON fields for this API action.
type secretWriteRequest struct {
	Value     string `json:"value"`
	RequestID string `json:"requestId"`
	Owner     string `json:"owner,omitempty"`
}
