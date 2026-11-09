package controller

import (
	"crypto/ed25519"
	"encoding/base64"
	"hostlier/internal/emailauth"
	"hostlier/internal/model"
	"net/http"
)

// pollCLIPairing lets only the initiating device collect an explicitly approved credential.
func (c *Controller) pollCLIPairing(r *http.Request) (any, error) {
	var b struct {
		ID        string `json:"id"`
		Secret    string `json:"requestSecret"`
		Signature string `json:"signature"`
	}
	// Unknown fields and malformed bodies cannot advance enrollment.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	p, err := c.livePairing(b.ID)
	// Expiry bounds pending polling even when the browser is abandoned.
	if err != nil {
		return nil, err
	}
	key, _ := emailauth.PublicKey(p.PublicKey)
	signature, _ := base64.RawStdEncoding.DecodeString(b.Signature)
	message := "hostlier-cli-pair-v1|" + c.state.HumanRealm + "|" + p.ID + "|" + p.Challenge
	// An opaque request ID or browser approval alone cannot collect credentials.
	if verifier(b.Secret) != p.SecretHash || !ed25519.Verify(key, []byte(message), signature) {
		return nil, model.Err("INVALID_ENROLLMENT", "CLI connection proof is invalid")
	}
	// Repeated pending polls neither issue credentials nor consume code attempts.
	if p.CodeHash == "" {
		return map[string]string{"status": "pending"}, nil
	}
	return c.completeCLIPairing(p)
}
