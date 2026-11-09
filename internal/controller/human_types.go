package controller

import (
	"encoding/json"
	"time"

	"hostlier/internal/securitykey"
)

// HumanIdentity holds independent hardware-key authority enrolled through protected administration.
type HumanIdentity struct {
	Passkeys map[string]Passkey                `json:"passkeys,omitempty"`
	ID       string                            `json:"id"`
	Name     string                            `json:"name"`
	Keys     map[string]securitykey.Credential `json:"keys"`
}

// HumanApproval binds a durable authorization to one actor and exact request.
type HumanApproval struct {
	AuthorizationID string          `json:"authorizationId,omitempty"`
	ID              string          `json:"id"`
	Code            string          `json:"code"`
	Actor           string          `json:"actor"`
	Method          string          `json:"method"`
	Path            string          `json:"path"`
	Payload         string          `json:"payload"`
	Fingerprint     string          `json:"fingerprint"`
	Generation      int64           `json:"generation"`
	ExpiresAt       int64           `json:"expiresAt"`
	Status          string          `json:"status"`
	Review          json.RawMessage `json:"review"`
	Approver        string          `json:"approver,omitempty"`
	CredentialID    string          `json:"credentialId,omitempty"`
	ApprovedAt      int64           `json:"approvedAt,omitempty"`
	Result          string          `json:"result,omitempty"`
	Failure         string          `json:"failure,omitempty"`
}

// humanCeremony is server-only challenge state and never survives a process restart.
type humanCeremony struct {
	ApprovalID     string
	Fingerprint    string
	Decision       string
	KeyFingerprint string
	Message        string
	ExpiresAt      time.Time
}

// humanSecurity retains one-time signing challenges only in memory.
type humanSecurity struct{ Ceremonies map[string]humanCeremony }

// humanRequest is the bounded original operation submitted for independent review.
type humanRequest struct {
	Target *fleetBinding   `json:"target,omitempty"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}
