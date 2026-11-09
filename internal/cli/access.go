package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hostlier/internal/client"
	"hostlier/internal/model"
)

// enrollBrowserCLI keeps the private device key local while the owner reviews its scope in the browser.
func (s *session) enrollBrowserCLI(remote Requester, name string) (enrollmentCredential, error) {
	var credential enrollmentCredential
	// A code must be entered in the initiating terminal, never supplied as a command-line argument.
	if !s.interactive {
		return credential, model.Err("INTERACTIVE_REQUIRED", "Controller pairing requires an interactive terminal")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	// Randomness failure cannot produce a reusable deployment identity.
	if err != nil {
		return credential, err
	}
	var pending struct {
		ID                 string `json:"id"`
		RequestSecret      string `json:"requestSecret"`
		Message            string `json:"message"`
		URL                string `json:"url"`
		PollSupported      bool   `json:"pollSupported"`
		OwnerSetupRequired bool   `json:"ownerSetupRequired"`
	}
	// The controller receives only the public half of the device credential.
	if err = remote.Request(s.ctx, "POST", "/v1/access/pair/start", map[string]string{"name": name, "publicKey": base64.RawStdEncoding.EncodeToString(public)}, &pending); err != nil {
		return credential, err
	}
	fmt.Fprintf(s.errOut, "Request ID: %s\nDevice fingerprint: %s\n", pending.ID, devicePublicFingerprint(public))
	// First-owner setup and normal approval share one initiating device request.
	pending.URL, err = s.prepareOwnerSetup(pending.ID, pending.URL, pending.PollSupported, pending.OwnerSetupRequired)
	// Cancelled or failed bootstrap leaves all existing profiles unchanged.
	if err != nil {
		return credential, err
	}
	fmt.Fprintf(s.errOut, "Open the controller to complete owner setup or review this CLI and choose all access or deployment limits:\n%s\n", pending.URL)
	proof := map[string]string{"id": pending.ID, "requestSecret": pending.RequestSecret, "signature": base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, []byte(pending.Message)))}
	credential, err = s.finishBrowserPairing(remote, pending.URL, pending.PollSupported, proof)
	// Cancellation leaves the saved profile untouched.
	if err != nil {
		return credential, err
	}
	// Reject incomplete issuance before wrapping a credential for durable storage.
	if credential.Token == "" || credential.ClientID == "" {
		return credential, model.Err("ENROLLMENT", "Controller returned no device credential")
	}
	credential.Token = client.EncodeDeviceCredential(credential.Token, private)
	return credential, nil
}

// pairingCode uses the protected server socket as the explicit administrator bootstrap path.
func (s *session) pairingCode() error {
	// Pairing approval still requires the protected local administration transport.
	if s.flags["socket"] == "" || s.target == "" {
		return model.Err("ARGUMENTS", "Usage: pairing code ID --socket PATH [--app APP --for 2h --max-uses 20 --secrets NAME --create-secrets NAME]; omit permission flags for all access")
	}
	scope, err := s.pairingScope()
	// A partial or malformed selection must never become all access.
	if err != nil {
		return err
	}
	var result any
	// The controller validates existence, exact names and all finite bounds.
	if err = s.request("POST", "/v1/access/pair/code", map[string]any{"id": s.target, "scope": scope}, &result); err != nil {
		return err
	}
	return s.print(result)
}

// passkeyEnrollment prints a short-lived browser bootstrap URL for the server owner.
func (s *session) passkeyEnrollment() error {
	var result any
	// The API independently requires local administration even if a caller bypasses CLI checks.
	if err := s.request("POST", "/v1/access/bootstrap", map[string]string{"name": defaultValue(s.flags["name"], "Owner")}, &result); err != nil {
		return err
	}
	return s.print(result)
}

// devicePublicFingerprint matches the controller review without exposing private key material.
func devicePublicFingerprint(public ed25519.PublicKey) string {
	sum := sha256.Sum256([]byte(base64.RawStdEncoding.EncodeToString(public)))
	return hex.EncodeToString(sum[:])
}

// bootstrapPairingURL requires automatic completion before beginning first-owner enrollment.
func (s *session) bootstrapPairingURL(id string, pollSupported bool) (string, error) {
	// Older controllers must be upgraded rather than entering a partial bootstrap flow.
	if !pollSupported {
		return "", model.Err("CONTROLLER_UPGRADE", "Update the controller to support SSH owner setup and automatic pairing")
	}
	return s.setupOwnerSSH(id)
}
