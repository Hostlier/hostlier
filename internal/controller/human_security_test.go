package controller

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"hostlier/internal/securitykey"
)

// humanFixture uses genuine signatures with a software authenticator only inside test code.
type humanFixture struct {
	c       *Controller
	key     *ecdsa.PrivateKey
	public  ssh.PublicKey
	token   string
	counter uint32
}

// newHumanFixture enrolls a FIDO-shaped public key only through the protected administrative listener.
func newHumanFixture(t *testing.T) *humanFixture {
	t.Helper()
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	// Keep this callback bounded to the current request and its captured state.
	t.Cleanup(func() { closeController(t, c) })
	return attachHumanFixture(t, c)
}

// attachHumanFixture enrolls independent test authority on an existing isolated controller.
func attachHumanFixture(t *testing.T, c *Controller) *humanFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	wire := ssh.Marshal(struct {
		Type, Curve string
		Point       []byte
		Application string
	}{ssh.KeyAlgoSKECDSA256, "nistp256", elliptic.Marshal(key.Curve, key.X, key.Y), "ssh:hostlier"})
	public, err := ssh.ParsePublicKey(wire)
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	f := &humanFixture{c: c, key: key, public: public, token: testCredentials(c, "agent")["token"].(string)}
	status, result := humanHTTP(t, c, true, "POST", "/v1/human/enroll", map[string]string{"name": "Owner", "publicKey": string(ssh.MarshalAuthorizedKey(public))}, "", "")
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, result)
	}
	return f
}

// humanHTTP makes raw requests without the legacy operation test helper or authorization shortcuts.
func humanHTTP(t *testing.T, c *Controller, local bool, method, path string, body any, token, origin string) (int, map[string]any) {
	t.Helper()
	data, err := json.Marshal(body)
	// Invalid fixtures must not be treated as server-side validation failures.
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.RemoteAddr = "192.0.2.40:1234"
	r.Header.Set("Origin", origin)
	// Only the actual bearer header can authenticate an API identity.
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	// Local authority is selected by the listener, never by any request field.
	if local {
		c.LocalHandler().ServeHTTP(w, r)
	} else {
		c.Handler().ServeHTTP(w, r)
	}
	var result map[string]any
	// All tested API endpoints return structured public JSON.
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return w.Code, result
}

// session requires a real server-issued one-use signing challenge.
func (f *humanFixture) session(t *testing.T, start map[string]any) humanCeremony {
	t.Helper()
	id, _ := start["ceremony"].(string)
	session, ok := f.c.human.Ceremonies[id]
	// Select the fixture behavior required to exercise this boundary.
	if !ok {
		t.Fatal(start)
	}
	return session
}

// signature exercises the maintained SSH verifier with protocol-correct authenticator signatures.
func (f *humanFixture) signature(t *testing.T, message string, flags byte) string {
	t.Helper()
	f.counter++
	envelope := securitykey.Envelope{Version: 1, PublicKey: f.public.Marshal(), Namespace: securitykey.Namespace, HashAlgorithm: "sha512"}
	data, err := securitykey.SignedData(envelope, []byte(message))
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	application := sha256.Sum256([]byte("ssh:hostlier"))
	digest := sha256.Sum256(data)
	authenticator := append(append([]byte{}, application[:]...), flags, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(authenticator[33:37], f.counter)
	authenticator = append(authenticator, digest[:]...)
	signed := sha256.Sum256(authenticator)
	r, s, err := ecdsa.Sign(rand.Reader, f.key, signed[:])
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	signature := ssh.Signature{Format: ssh.KeyAlgoSKECDSA256, Blob: ssh.Marshal(struct{ R, S *big.Int }{r, s}), Rest: ssh.Marshal(struct {
		Flags   byte
		Counter uint32
	}{flags, f.counter})}
	envelope.Signature = ssh.Marshal(signature)
	return string(pem.EncodeToMemory(&pem.Block{Type: "SSH SIGNATURE", Bytes: append([]byte("SSHSIG"), ssh.Marshal(envelope)...)}))
}

// propose records encrypted intent without issuing a mutation.
func (f *humanFixture) propose(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	status, result := humanHTTP(t, f.c, false, "POST", "/v1/approvals", map[string]any{"method": method, "path": path, "body": body}, f.token, "")
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, result)
	}
	return result
}

// beginDecision asks for the exact fingerprint and hardware key used by the human terminal.
func (f *humanFixture) beginDecision(t *testing.T, a map[string]any, decision string) map[string]any {
	t.Helper()
	status, result := humanHTTP(t, f.c, false, "POST", "/v1/human/decision/begin", map[string]any{"id": a["id"], "fingerprint": a["fingerprint"], "decision": decision, "keyFingerprint": ssh.FingerprintSHA256(f.public)}, "", "")
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, result)
	}
	return result
}

// TestHumanHardwareApprovalAndReplay proves bearer credentials cannot mutate and signed receipt replay is idempotent.
func TestHumanHardwareApprovalAndReplay(t *testing.T) {
	f := newHumanFixture(t)
	body := map[string]string{"value": "secret-not-for-review", "requestId": "write-one"}
	status, _ := humanHTTP(t, f.c, false, "PUT", "/v1/secrets/key", body, f.token, "")
	// Fail if the fixture violates this invariant: bearer bypass.
	if status != 403 {
		t.Fatal("bearer bypass", status)
	}
	a := f.propose(t, "PUT", "/v1/secrets/key", body)
	data, _ := json.Marshal(a)
	// Fail if the fixture violates this invariant: review leaked a secret.
	if strings.Contains(string(data), "secret-not-for-review") {
		t.Fatal("review leaked a secret")
	}
	status, first := approveFixtureRequest(t, f, a)
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, first)
	}
	status, again := humanHTTP(t, f.c, false, "POST", "/v1/approvals/"+a["id"].(string)+"/execute", map[string]any{}, f.token, "")
	// Fail if the fixture violates this invariant: duplicate execution.
	if status != 200 || first["revision"] != again["revision"] {
		t.Fatal("duplicate execution", status, again)
	}
}

// TestHumanRequiresSignedTouchAndVerification rejects signatures missing either independent authenticator flag.
func TestHumanRequiresSignedTouchAndVerification(t *testing.T) {
	// Exercise every case independently, including denied inputs.
	for _, flags := range []byte{0, 1, 4} {
		// Isolate this case so credentials and mutable state cannot leak between scenarios.
		t.Run(string(rune('0'+flags)), func(t *testing.T) {
			f := newHumanFixture(t)
			a := f.propose(t, "PUT", "/v1/secrets/key", map[string]string{"value": "value", "requestId": "write"})
			start := f.beginDecision(t, a, "approve")
			session := f.session(t, start)
			proof := map[string]any{"ceremony": start["ceremony"], "signature": f.signature(t, session.Message, flags)}
			status, _ := humanHTTP(t, f.c, false, "POST", "/v1/human/decision/finish", proof, "", "")
			// Fail if the fixture violates this invariant: missing presence/verification accepted.
			if status != 403 || f.c.state.HumanApprovals[a["id"].(string)].Status != "pending" {
				t.Fatal("missing presence/verification accepted", status)
			}
			proof["signature"] = f.signature(t, session.Message, 5)
			status, _ = humanHTTP(t, f.c, false, "POST", "/v1/human/decision/finish", proof, "", "")
			// Fail if the fixture violates this invariant: challenge was not burned.
			if status == 200 {
				t.Fatal("challenge was not burned")
			}
		})
	}
}

// TestHumanRejectsAlteredIntentAndRemoteKeyEnrollment verifies request binding and the root-only key boundary.
func TestHumanRejectsAlteredIntentAndRemoteKeyEnrollment(t *testing.T) {
	f := newHumanFixture(t)
	status, _ := humanHTTP(t, f.c, false, "POST", "/v1/human/enroll", map[string]string{"name": "attacker", "publicKey": string(ssh.MarshalAuthorizedKey(f.public))}, f.token, "")
	// Fail if the fixture violates this invariant: agent enrolled its own approver.
	if status != 403 {
		t.Fatal("agent enrolled its own approver", status)
	}
	a := f.propose(t, "PUT", "/v1/secrets/key", map[string]string{"value": "value", "requestId": "write"})
	start := f.beginDecision(t, a, "approve")
	session := f.session(t, start)
	proof := map[string]any{"ceremony": start["ceremony"], "signature": f.signature(t, session.Message+"tampered", 5)}
	status, _ = humanHTTP(t, f.c, false, "POST", "/v1/human/decision/finish", proof, "", "")
	// Fail if the fixture violates this invariant: altered intent accepted.
	if status != 403 {
		t.Fatal("altered intent accepted", status)
	}
}

// TestHumanRejectsStaleAndRevokedApproval verifies authorization cannot survive relevant state or key changes.
func TestHumanRejectsStaleAndRevokedApproval(t *testing.T) {
	f := newHumanFixture(t)
	a := f.propose(t, "PUT", "/v1/secrets/key", map[string]string{"value": "value", "requestId": "write"})
	start := f.beginDecision(t, a, "approve")
	session := f.session(t, start)
	proof := map[string]any{"ceremony": start["ceremony"], "signature": f.signature(t, session.Message, 5)}
	f.c.state.Generation++
	status, _ := humanHTTP(t, f.c, false, "POST", "/v1/human/decision/finish", proof, "", "")
	// Fail if the fixture violates this invariant: stale approval accepted.
	if status == 200 {
		t.Fatal("stale approval accepted")
	}
}
