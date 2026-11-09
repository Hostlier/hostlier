package controller

import (
	"github.com/go-webauthn/webauthn/webauthn"
	"time"
)

// Passkey retains the verifier and mutable authenticator state, never private key material.
type Passkey struct {
	Credential webauthn.Credential `json:"credential"`
	Revoked    bool                `json:"revoked"`
}

// browserCeremony binds a WebAuthn challenge to one immutable operation or bootstrap token.
type browserCeremony struct {
	Session                                                    webauthn.SessionData
	Kind, Bootstrap, ApprovalID, Fingerprint, Decision, PairID string
	Scope                                                      deploymentScope
	Generation                                                 int64
	ExpiresAt                                                  time.Time
}

// accessState bounds ephemeral ceremonies and pairing codes; restart invalidates all of them.
type accessState struct {
	Ceremonies map[string]browserCeremony
	Bootstraps map[string]time.Time
	Pairings   map[string]*cliPairing
}

// WebAuthnID supplies a stable opaque owner identity scoped to this controller.
func (h *HumanIdentity) WebAuthnID() []byte { return []byte(h.ID) }

// WebAuthnName labels the owner in the authenticator UI.
func (h *HumanIdentity) WebAuthnName() string { return h.Name }

// WebAuthnDisplayName labels the owner in the browser.
func (h *HumanIdentity) WebAuthnDisplayName() string { return h.Name }

// WebAuthnCredentials excludes revoked keys from registration exclusions and assertions.
func (h *HumanIdentity) WebAuthnCredentials() []webauthn.Credential {
	result := []webauthn.Credential{}
	// Revoked credentials must never authenticate subsequent approvals.
	for _, key := range h.Passkeys {
		// Proceed only for this explicitly validated protocol state.
		if !key.Revoked {
			result = append(result, key.Credential)
		}
	}
	return result
}
