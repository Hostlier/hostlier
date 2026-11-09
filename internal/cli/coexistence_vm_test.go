package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"hostlier/internal/client"
)

const coexistenceFixtureRoot = "/root/hostlier-coexistence-e2e"

// coexistenceSignerState persists only a disposable software authenticator used by this VM test.
type coexistenceSignerState struct {
	Private []byte `json:"private"`
	Counter uint32 `json:"counter"`
}

// TestCoexistenceVMApproval exercises the installed controller's signature protocol, never a production bypass.
func TestCoexistenceVMApproval(t *testing.T) {
	// The test requires both explicit opt-in and a marker created inside the dedicated disposable VM.
	if os.Getenv("HOSTLIER_COEXISTENCE_VM") != "1" {
		t.Skip("disposable coexistence VM only")
	}
	marker, err := os.ReadFile(coexistenceFixtureRoot + "/fixture")
	// Existing servers and ordinary developer machines cannot accidentally enroll this test identity.
	if err != nil || string(marker) != "hostlier-coexistence-disposable-v1\n" || os.Geteuid() != 0 {
		t.Fatal("not the disposable coexistence fixture")
	}
	remote, err := client.New(client.Options{SocketPath: "/run/hostlier/controller.sock"})
	// The real installed controller is mandatory; no injected transport is used.
	if err != nil {
		t.Fatal(err)
	}
	code := os.Getenv("HOSTLIER_COEXISTENCE_APPROVAL")
	// Enrollment happens before the CLI creates plans so it cannot invalidate a pending request.
	if code == "" {
		enrollCoexistenceSigner(t, remote)
		return
	}
	f, state := loadCoexistenceSigner(t)
	approveCoexistenceRequest(t, remote, f, code)
	state.Counter = f.counter
	saveCoexistenceSigner(t, state)
}

// coexistencePublic constructs the same standards-shaped test key as the existing independent approval fixtures.
func coexistencePublic(t *testing.T, key *ecdsa.PrivateKey) ssh.PublicKey {
	t.Helper()
	public, err := ssh.ParsePublicKey(ssh.Marshal(struct {
		Type, Curve string
		Point       []byte
		Application string
	}{
		ssh.KeyAlgoSKECDSA256, "nistp256", elliptic.Marshal(key.Curve, key.X, key.Y), "ssh:hostlier",
	}))
	// A malformed fixture key must fail before enrollment or signing.
	if err != nil {
		t.Fatal(err)
	}
	return public
}

// enrollCoexistenceSigner creates a fresh test-only identity through the protected local enrollment endpoint.
func enrollCoexistenceSigner(t *testing.T, remote *client.Client) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Test authority must still use securely generated key material.
	if err != nil {
		t.Fatal(err)
	}
	public := coexistencePublic(t, key)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var result any
	err = remote.Request(ctx, "POST", "/v1/human/enroll", map[string]string{"name": "Disposable coexistence test authenticator", "publicKey": string(ssh.MarshalAuthorizedKey(public))}, &result)
	// The production controller must accept enrollment through its normal root-only API.
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	// Persist only complete fixture keys in the root-owned test directory.
	if err != nil {
		t.Fatal(err)
	}
	saveCoexistenceSigner(t, coexistenceSignerState{Private: private})
}

// saveCoexistenceSigner retains the monotonic test counter without printing private material.
func saveCoexistenceSigner(t *testing.T, state coexistenceSignerState) {
	t.Helper()
	data, _ := json.Marshal(state)
	// The fixture key never leaves this disposable guest or becomes part of the release binary.
	if err := os.WriteFile(coexistenceFixtureRoot+"/approver.json", data, 0600); err != nil {
		t.Fatal(err)
	}
}

// loadCoexistenceSigner resumes the test authenticator's counter between real CLI invocations.
func loadCoexistenceSigner(t *testing.T) (*fixtureHuman, coexistenceSignerState) {
	t.Helper()
	data, err := os.ReadFile(coexistenceFixtureRoot + "/approver.json")
	// A missing identity must not trigger enrollment after a plan already exists.
	if err != nil {
		t.Fatal(err)
	}
	var state coexistenceSignerState
	// Invalid retained test state cannot authorize a request.
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParseECPrivateKey(state.Private)
	// Parse the private key before constructing a standards-shaped public identity.
	if err != nil {
		t.Fatal(err)
	}
	return &fixtureHuman{key: key, public: coexistencePublic(t, key), counter: state.Counter}, state
}

// coexistenceReview decodes only the controller-retained intent needed to constrain the test approval.
type coexistenceReview struct {
	ID, Fingerprint string
	Review          struct {
		Method, Path string
		Plan         struct{ Spec struct{ Name string } }
	}
}

// approveCoexistenceRequest signs only this fixture's app requests through the real challenge and verification APIs.
func approveCoexistenceRequest(t *testing.T, remote *client.Client, f *fixtureHuman, code string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var review coexistenceReview
	// Fetch retained intent rather than trusting a caller-supplied request body.
	if err := remote.Request(ctx, "GET", "/v1/human/review/"+code, nil, &review); err != nil {
		t.Fatal(err)
	}
	// This fixture cannot sign unrelated server administration or application requests.
	if !coexistenceReviewAllowed(review) {
		t.Fatal("request is outside the disposable coexistence fixture")
	}
	var ceremony struct {
		ID      string `json:"ceremony"`
		Message string `json:"message"`
	}
	body := map[string]string{"id": review.ID, "fingerprint": review.Fingerprint, "decision": "approve", "keyFingerprint": ssh.FingerprintSHA256(f.public)}
	// A valid live challenge must bind the exact reviewed fingerprint.
	if err := remote.Request(ctx, "POST", "/v1/human/decision/begin", body, &ceremony); err != nil {
		t.Fatal(err)
	}
	var result any
	// The installed controller still verifies the signature, signed flags, counter and nonce.
	if err := remote.Request(ctx, "POST", "/v1/human/decision/finish", map[string]string{"ceremony": ceremony.ID, "signature": f.sign(ceremony.Message)}, &result); err != nil {
		t.Fatal(err)
	}
}

// coexistenceReviewAllowed confines test authority to the two fixture applications and their lifecycle.
func coexistenceReviewAllowed(review coexistenceReview) bool {
	// No read request or arbitrary administration call requires this test signer.
	if review.Review.Method != "POST" {
		return false
	}
	// Only the new test app has lifecycle operations in this rehearsal.
	if review.Review.Path == "/v1/apps/coexist-new/action" {
		return true
	}
	name := review.Review.Plan.Spec.Name
	return strings.HasPrefix(review.Review.Path, "/v1/plans/") && strings.HasSuffix(review.Review.Path, "/apply") && (name == "coexist-new" || name == "coexist-conflict")
}
