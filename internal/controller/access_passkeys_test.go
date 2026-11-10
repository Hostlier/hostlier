package controller

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"github.com/fxamacker/cbor/v2"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// browserFixture uses a software authenticator only in tests to exercise real WebAuthn verification.
type browserFixture struct {
	c       *Controller
	key     *ecdsa.PrivateKey
	id      []byte
	counter uint32
	rpID    string // Invalid-RP proofs must not mutate the live controller's hostname.
}

// browserJSON sends same-origin browser ceremony requests through the production HTTP handler.
func browserJSON(t *testing.T, c *Controller, path string, body any, ceremony string) (int, map[string]any) {
	t.Helper()
	data, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(data))
	r.Header.Set("Origin", "https://"+c.state.Hostname)
	r.Header.Set("X-Hostlier-Ceremony", ceremony)
	w := httptest.NewRecorder()
	c.Handler().ServeHTTP(w, r)
	var result map[string]any
	// Fail the test when the expected security invariant is violated.
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err, w.Body.String())
	}
	return w.Code, result
}

// authenticatorData builds signed RP, presence, verification and counter fields.
func (f *browserFixture) authenticatorData(flags byte) []byte {
	hostname := f.c.state.Hostname
	// A software authenticator can sign the wrong RP without changing controller state.
	if f.rpID != "" {
		hostname = f.rpID
	}
	hash := sha256.Sum256([]byte(hostname))
	data := append([]byte{}, hash[:]...)
	data = append(data, flags)
	return binary.BigEndian.AppendUint32(data, f.counter)
}

// clientData retains the exact browser challenge and origin in signed bytes.
func (f *browserFixture) clientData(v browserCeremony, kind, origin string) []byte {
	data, _ := json.Marshal(map[string]any{"type": kind, "challenge": v.Session.Challenge, "origin": origin, "crossOrigin": false})
	return data
}

// registerBrowserFixture performs administrator bootstrap and a genuine none-attestation ceremony.
func registerBrowserFixture(t *testing.T) *browserFixture {
	t.Helper()
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	// Keep simulated transport and user input confined to the test.
	t.Cleanup(func() { closeController(t, c) })
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := &browserFixture{c: c, key: key, id: []byte("test-passkey-credential"), counter: 0}
	_, bootstrap := humanHTTP(t, c, true, "POST", "/v1/access/bootstrap", map[string]string{"name": "Owner"}, "", "")
	u, _ := url.Parse(bootstrap["url"].(string))
	token := strings.TrimPrefix(u.Fragment, "enroll=")
	status, start := browserJSON(t, c, "/v1/access/passkey/register/begin", map[string]string{"token": token}, "")
	// Fail the test when the expected security invariant is violated.
	if status != 200 {
		t.Fatal(status, start)
	}
	ceremony := start["ceremony"].(string)
	session := c.access.Ceremonies[ceremony]
	cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
	auth := f.authenticatorData(0x45)
	auth = append(auth, make([]byte, 16)...)
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(f.id)))
	auth = append(auth, f.id...)
	auth = append(auth, cose...)
	attestation, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	body := map[string]any{"id": base64.RawURLEncoding.EncodeToString(f.id), "rawId": base64.RawURLEncoding.EncodeToString(f.id), "type": "public-key", "response": map[string]any{"clientDataJSON": base64.RawURLEncoding.EncodeToString(f.clientData(session, "webauthn.create", "https://"+c.state.Hostname)), "attestationObject": base64.RawURLEncoding.EncodeToString(attestation)}}
	status, result := browserJSON(t, c, "/v1/access/passkey/register/finish", body, ceremony)
	// Fail the test when the expected security invariant is violated.
	if status != 200 {
		t.Fatal(status, result)
	}
	return f
}

// assertion signs the exact server challenge with independently selectable UV and origin boundaries.
func (f *browserFixture) assertion(t *testing.T, ceremony string, flags byte, origin string) map[string]any {
	t.Helper()
	f.counter++
	auth := f.authenticatorData(flags)
	data := f.clientData(f.c.access.Ceremonies[ceremony], "webauthn.get", origin)
	hash := sha256.Sum256(data)
	signed := sha256.Sum256(append(auth, hash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, f.key, signed[:])
	// Fail the test when the expected security invariant is violated.
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return map[string]any{"id": enc(f.id), "rawId": enc(f.id), "type": "public-key", "response": map[string]any{"clientDataJSON": enc(data), "authenticatorData": enc(auth), "signature": enc(signature), "userHandle": enc([]byte(f.c.state.Human.ID))}}
}

// browserDecisionFixture retains a real exact administrative request for passkey review.
func (f *browserFixture) browserDecisionFixture(t *testing.T) string {
	t.Helper()
	token := testCredentials(f.c, "agent")["token"].(string)
	status, a := humanHTTP(t, f.c, false, "POST", "/v1/approvals", map[string]any{"method": "POST", "path": "/v1/pairing/tokens", "body": map[string]string{"name": "future"}}, token, "")
	// Fail the test when the expected security invariant is violated.
	if status != 200 {
		t.Fatal(status, a)
	}
	status, start := browserJSON(t, f.c, "/v1/access/passkey/decision/begin", map[string]any{"id": a["id"], "fingerprint": a["fingerprint"], "decision": "approve"}, "")
	// Fail the test when the expected security invariant is violated.
	if status != 200 {
		t.Fatal(status, start)
	}
	return start["ceremony"].(string)
}

// TestBrowserPasskeyApproval verifies actual WebAuthn signatures and replay resistance without hardware enrollment.
func TestBrowserPasskeyApproval(t *testing.T) {
	f := registerBrowserFixture(t)
	id := f.browserDecisionFixture(t)
	proof := f.assertion(t, id, 5, "https://"+f.c.state.Hostname)
	status, result := browserJSON(t, f.c, "/v1/access/passkey/decision/finish", proof, id)
	// Fail the test when the expected security invariant is violated.
	if status != 200 || result["status"] != "approved" {
		t.Fatal(status, result)
	}
	status, _ = browserJSON(t, f.c, "/v1/access/passkey/decision/finish", proof, id)
	// Fail the test when the expected security invariant is violated.
	if status == 200 {
		t.Fatal("passkey replay accepted")
	}
	// Fail the test when the expected security invariant is violated.
	if len(f.c.state.Human.Keys) != 0 {
		t.Fatal("unexpected hardware enrollment")
	}
}

// TestBrowserPasskeyRejectsUnverifiedOriginAndRevocation checks fail-closed proof boundaries.
func TestBrowserPasskeyRejectsUnverifiedOriginAndRevocation(t *testing.T) {
	// Exercise this protocol boundary independently.
	for _, mode := range []string{"no-uv", "wrong-origin", "revoked", "stale", "wrong-challenge", "wrong-rp"} {
		// Keep simulated transport and user input confined to the test.
		t.Run(mode, func(t *testing.T) {
			f := registerBrowserFixture(t)
			id := f.browserDecisionFixture(t)
			flags := byte(5)
			origin := "https://" + f.c.state.Hostname
			// Fail the test when the expected security invariant is violated.
			if mode == "no-uv" {
				flags = 1
			}
			// Fail the test when the expected security invariant is violated.
			if mode == "wrong-origin" {
				origin = "https://evil.example"
			}
			// Fail the test when the expected security invariant is violated.
			if mode == "wrong-challenge" {
				v := f.c.access.Ceremonies[id]
				v.Session.Challenge = "wrong"
				f.c.access.Ceremonies[id] = v
			}
			// Fail the test when the expected security invariant is violated.
			if mode == "wrong-rp" {
				f.rpID = "evil.example"
			}
			proof := f.assertion(t, id, flags, origin)
			// Fail the test when the expected security invariant is violated.
			if mode == "wrong-challenge" {
				v := f.c.access.Ceremonies[id]
				v.Session.Challenge = "expected"
				f.c.access.Ceremonies[id] = v
			}
			mutateBrowserAuthority(f, mode)
			status, result := browserJSON(t, f.c, "/v1/access/passkey/decision/finish", proof, id)
			// Fail the test when the expected security invariant is violated.
			if status == 200 {
				t.Fatal("invalid assertion accepted", result)
			}
		})
	}
}

// mutateBrowserAuthority simulates authority changes between challenge and assertion.
func mutateBrowserAuthority(f *browserFixture, mode string) {
	// Background persistence reads authority and generation under this same lock.
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	// Revocation must invalidate all assertions for the affected credential.
	if mode == "revoked" {
		// Exercise this protocol boundary independently.
		for key, p := range f.c.state.Human.Passkeys {
			p.Revoked = true
			f.c.state.Human.Passkeys[key] = p
		}
	}
	// An intervening controller change forces fresh review of intent.
	if mode == "stale" {
		f.c.state.Generation++
	}
}
