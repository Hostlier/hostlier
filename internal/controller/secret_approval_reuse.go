package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// recentCompletedSecretApproval reuses an exact, successful instance-secret
// registration by the same API identity. The requested value must still be the
// current stored revision, and a distinct request ID gets its own replay receipt.
func (c *Controller) recentCompletedSecretApproval(request humanRequest, actor string) (*HumanApproval, error) {
	if request.Method != http.MethodPut || !strings.HasPrefix(request.Path, "/v1/secrets/") {
		return nil, nil
	}
	name := strings.TrimPrefix(request.Path, "/v1/secrets/")
	if required(name) != nil || strings.ContainsAny(name, "/\\") {
		return nil, nil
	}
	incoming, ok := secretApprovalBody(request.Body)
	if !ok || incoming.Owner == "" {
		return nil, nil
	}
	secret := c.state.Secrets[name]
	if secret == nil || secret.Owner != incoming.Owner {
		return nil, nil
	}
	revision, err := c.unchangedSecretRevision(name, incoming.Value)
	if err != nil || revision == "" {
		return nil, err
	}
	key := verifier(actor + "|" + incoming.RequestID)
	signature := c.store.Authenticate(name + "|" + incoming.Value)
	if write := c.state.Writes[key]; write.Signature != "" && (write.Signature != signature || write.Name != name || write.Revision != revision) {
		return nil, nil // Let normal execution report the idempotency conflict.
	}
	for _, approval := range c.state.HumanApprovals {
		if approval.Actor != actor || approval.Method != request.Method || approval.Path != request.Path || approval.Status != "completed" || approval.ExpiresAt <= c.opts.Now().UnixMilli() {
			continue
		}
		prior, err := c.openHumanRequest(approval)
		if err != nil {
			return nil, err
		}
		original, ok := secretApprovalBody(prior.Body)
		if !ok || original.Owner != incoming.Owner || original.Value != incoming.Value {
			continue
		}
		result, err := c.readHumanResult(approval)
		if err != nil {
			return nil, err
		}
		receipt, ok := result.(map[string]any)
		if !ok || receipt["name"] != name || receipt["revision"] != revision {
			continue
		}
		if c.state.Writes == nil {
			c.state.Writes = map[string]Write{}
		}
		c.state.Writes[key] = Write{Signature: signature, Name: name, Revision: revision}
		return approval, nil
	}
	return nil, nil
}

func secretApprovalBody(raw json.RawMessage) (secretWriteRequest, bool) {
	var body secretWriteRequest
	r := &http.Request{Body: io.NopCloser(bytes.NewReader(raw))}
	if decode(r, &body) != nil || len(body.Value) == 0 || len(body.Value) > 65536 || required(body.RequestID) != nil || !secretOwnerName.MatchString(body.Owner) || strings.HasSuffix(body.Owner, "-") {
		return secretWriteRequest{}, false
	}
	return body, true
}
