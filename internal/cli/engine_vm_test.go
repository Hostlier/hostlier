package cli

import (
	"context"
	"golang.org/x/crypto/ssh"
	"hostlier/internal/client"
	"os"
	"strings"
	"testing"
	"time"
)

// TestEngineVMApproval is an opt-in software authenticator confined to the disposable Engine fixture.
func TestEngineVMApproval(t *testing.T) {
	// No ordinary test or production server can activate this signing helper.
	if os.Getenv("HOSTLIER_ENGINE_VM") != "1" {
		t.Skip("disposable Engine VM only")
	}
	hostname, _ := os.Hostname()
	marker, err := os.ReadFile("/root/hostlier-engine-e2e/fixture")
	// Both root ownership and the explicit guest marker fence test authority.
	if err != nil || hostname != "lima-hostlier-ui-outage" || string(marker) != "hostlier-engine-e2e-v1\n" || os.Geteuid() != 0 {
		t.Fatal("wrong Engine fixture")
	}
	remote, err := client.New(client.Options{SocketPath: "/run/hostlier/controller.sock"})
	// A real installed controller must perform every verification step.
	if err != nil {
		t.Fatal(err)
	}
	approveEngineFixture(t, remote, os.Getenv("HOSTLIER_ENGINE_APPROVAL"))
}

// approveEngineFixture signs only reviewed Engine/protocol-probe plans and their finite secret names.
func approveEngineFixture(t *testing.T, remote *client.Client, code string) {
	t.Helper()
	approveEngineFixtureWithPolicy(t, remote, code, engineFixtureAllowed)
}

// approveEngineFixtureWithPolicy keeps software authority confined to each explicitly authorized disposable fixture.
func approveEngineFixtureWithPolicy(t *testing.T, remote *client.Client, code string, allowed func(coexistenceReview) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var review coexistenceReview
	// The controller-retained request supplies authority, never an unreviewed caller body.
	if err := remote.Request(ctx, "GET", "/v1/human/review/"+code, nil, &review); err != nil {
		t.Fatal(err)
	}
	// No unrelated application or administrator action can be signed by this test fixture.
	if !allowed(review) {
		t.Fatal("request outside Engine fixture")
	}
	f, state := loadCoexistenceSigner(t)
	var ceremony struct {
		ID      string `json:"ceremony"`
		Message string `json:"message"`
	}
	body := map[string]string{"id": review.ID, "fingerprint": review.Fingerprint, "decision": "approve", "keyFingerprint": ssh.FingerprintSHA256(f.public)}
	// Production challenge verification binds the signature to a fresh nonce and exact plan.
	if err := remote.Request(ctx, "POST", "/v1/human/decision/begin", body, &ceremony); err != nil {
		t.Fatal(err)
	}
	var result any
	// Flags, nonce and counters are verified by the real daemon.
	if err := remote.Request(ctx, "POST", "/v1/human/decision/finish", map[string]string{"ceremony": ceremony.ID, "signature": f.sign(ceremony.Message)}, &result); err != nil {
		t.Fatal(err)
	}
	state.Counter = f.counter
	saveCoexistenceSigner(t, state)
}

// engineFixtureAllowed is the complete finite mutation scope of this disposable verification.
func engineFixtureAllowed(review coexistenceReview) bool {
	// Secret writes require both a specific method and one fixed name.
	if review.Review.Method == "PUT" {
		return review.Review.Path == "/v1/secrets/fused-engine-license" || review.Review.Path == "/v1/secrets/fused-engine-encryption" || review.Review.Path == "/v1/secrets/fused-engine-database" || review.Review.Path == "/v1/secrets/fused-engine-postgres"
	}
	// Only exact deployment-plan application requests are otherwise allowed.
	if review.Review.Method != "POST" || !strings.HasPrefix(review.Review.Path, "/v1/plans/") || !strings.HasSuffix(review.Review.Path, "/apply") {
		return false
	}
	return review.Review.Plan.Spec.Name == "fused-engine" || review.Review.Plan.Spec.Name == "protocol-probe"
}
