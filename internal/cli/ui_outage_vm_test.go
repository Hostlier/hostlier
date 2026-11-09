package cli

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"hostlier/internal/client"
)

// TestUIOutageVMApproval supplies isolated software-authenticator proofs through production verification.
func TestUIOutageVMApproval(t *testing.T) {
	// Never enroll or sign outside the explicitly named disposable root-owned VM.
	if os.Getenv("HOSTLIER_UI_OUTAGE_VM") != "1" {
		t.Skip("disposable UI outage VM only")
	}
	hostname, _ := os.Hostname()
	marker, err := os.ReadFile("/root/hostlier-ui-outage/fixture")
	// The test marker and guest identity both fence this test-only signing authority.
	if err != nil || string(marker) != "hostlier-ui-outage-v1\n" || hostname != "lima-hostlier-ui-outage" || os.Geteuid() != 0 {
		t.Fatal("not the disposable UI outage fixture")
	}
	remote, err := client.New(client.Options{SocketPath: "/run/hostlier/controller.sock"})
	// A real protected controller socket is required rather than an injected HTTP handler.
	if err != nil {
		t.Fatal(err)
	}
	code := os.Getenv("HOSTLIER_UI_OUTAGE_APPROVAL")
	// Enroll a test key only before any reviewed deployment plans exist.
	if code == "" {
		enrollCoexistenceSigner(t, remote)
		return
	}
	signUIOutageRequest(t, remote, code)
}

// signUIOutageRequest signs only fixture enrollment, ingestion setup and its one real workload.
func signUIOutageRequest(t *testing.T, remote *client.Client, code string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var review coexistenceReview
	// Read the exact controller-retained intent before applying the fixture allowlist.
	if err := remote.Request(ctx, "GET", "/v1/human/review/"+code, nil, &review); err != nil {
		t.Fatal(err)
	}
	path := review.Review.Path
	// Only these test setup requests or the existing one-app deployment boundary may be approved.
	if !coexistenceReviewAllowed(review) && !(review.Review.Method == "POST" && (path == "/v1/pairing/tokens" || path == "/v1/telemetry/collectors")) {
		t.Fatal("request outside the UI outage fixture")
	}
	f, state := loadCoexistenceSigner(t)
	var ceremony struct {
		ID      string `json:"ceremony"`
		Message string `json:"message"`
	}
	body := map[string]string{"id": review.ID, "fingerprint": review.Fingerprint, "decision": "approve", "keyFingerprint": ssh.FingerprintSHA256(f.public)}
	// Production verification binds the signature to a fresh nonce and the exact reviewed request.
	if err := remote.Request(ctx, "POST", "/v1/human/decision/begin", body, &ceremony); err != nil {
		t.Fatal(err)
	}
	var result any
	// Signed presence flags and the monotonic test counter are verified by the installed daemon.
	if err := remote.Request(ctx, "POST", "/v1/human/decision/finish", map[string]string{"ceremony": ceremony.ID, "signature": f.sign(ceremony.Message)}, &result); err != nil {
		t.Fatal(err)
	}
	state.Counter = f.counter
	saveCoexistenceSigner(t, state)
}
