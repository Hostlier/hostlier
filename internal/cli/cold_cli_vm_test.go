package cli

import (
	"hostlier/internal/client"
	"os"
	"testing"
)

// TestColdCLIVMApproval is the explicitly authorized software authenticator for the fresh disposable CLI test.
func TestColdCLIVMApproval(t *testing.T) {
	// The ordinary release CLI and normal test runs never expose software signing authority.
	if os.Getenv("HOSTLIER_COLD_CLI_VM") != "1" {
		t.Skip("explicit disposable CLI VM only")
	}
	host, _ := os.Hostname()
	marker, err := os.ReadFile("/root/hostlier-cold-cli-e2e/fixture")
	// Root identity, exact guest hostname and a private fixture marker fence all test authority.
	if err != nil || host != "lima-hostlier-cli-deploy" || string(marker) != "hostlier-cold-cli-e2e-v1\n" || os.Geteuid() != 0 {
		t.Fatal("wrong cold CLI fixture")
	}
	remote, err := client.New(client.Options{SocketPath: "/run/hostlier/controller.sock"})
	// Every enrollment, challenge and signature must pass the installed controller's real checks.
	if err != nil {
		t.Fatal(err)
	}
	code := os.Getenv("HOSTLIER_COLD_CLI_APPROVAL")
	// Enrollment precedes planning and creates a new key inside this guest only.
	if code == "" {
		enrollCoexistenceSigner(t, remote)
		return
	}
	approveEngineFixtureWithPolicy(t, remote, code, coldCLIFixtureAllowed)
}

// coldCLIFixtureAllowed limits fresh-VM authority to pairing setup and this Engine's reviewed secrets/deployments.
func coldCLIFixtureAllowed(review coexistenceReview) bool {
	// One-use client enrollment is part of the explicitly requested controller setup.
	if review.Review.Method == "POST" && review.Review.Path == "/v1/pairing/tokens" {
		return true
	}
	return engineFixtureAllowed(review)
}
