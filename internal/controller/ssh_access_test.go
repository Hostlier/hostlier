package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"hostlier/internal/client"
	"hostlier/internal/model"
)

// sshAccessProvider records host effects after authenticated approval dispatch.
type sshAccessProvider struct{ grants, revocations atomic.Int32 }

// Grant records only requests that crossed the independent approval boundary.
func (p *sshAccessProvider) Grant(context.Context, string, model.SSHAccessRequest) (model.SSHAccess, error) {
	p.grants.Add(1)
	return model.SSHAccess{User: "fixture"}, nil
}

// Revoke records account revocation without touching the test host.
func (p *sshAccessProvider) Revoke(context.Context, string) error { p.revocations.Add(1); return nil }

// TestManagedSSHRequiresApprovedOwner checks signed device authentication, direct-call denial and receipt replay.
func TestManagedSSHRequiresApprovedOwner(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	provider := &sshAccessProvider{}
	c.opts.SSHAccess = provider
	scoped, _, _ := pairedFixture(t, c)
	// Scoped deployment credentials cannot even prepare an SSH account approval.
	if err := scoped.Request(context.Background(), "POST", "/v1/approvals", map[string]any{"method": "POST", "path": "/v1/controller/ssh", "body": map[string]string{"publicKey": "invalid"}}, nil); err == nil {
		t.Fatal("scoped credential requested shell access")
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	paired, _, _ := pairWithKey(t, c, deploymentScope{}, pub, key)
	api, _, _ := grpcFixture(t, c, client.EncodeDeviceCredential(paired.Token, key))
	sshKey, _ := ssh.NewPublicKey(pub)
	input := model.SSHAccessRequest{PublicKey: string(ssh.MarshalAuthorizedKey(sshKey))}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := api.Request(context.Background(), "POST", "/v1/controller/ssh", input, nil); err == nil {
		t.Fatal("direct SSH grant bypassed approval")
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if provider.grants.Load() != 0 {
		t.Fatal("unapproved host mutation")
	}
	var approval map[string]any
	request := map[string]any{"method": "POST", "path": "/v1/controller/ssh", "body": input}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := api.Request(context.Background(), "POST", "/v1/approvals", request, &approval); err != nil {
		t.Fatal(err)
	}
	// Exercise each independent SSH input or selection case.
	for i := 0; i < 2; i++ {
		var result model.SSHAccess
		// Assert the SSH fixture outcome before advancing to the next boundary.
		if err := api.Request(context.Background(), "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, &result); err != nil {
			t.Fatal(err)
		}
		// Assert the SSH fixture outcome before advancing to the next boundary.
		if result.User != "fixture" {
			t.Fatal(result)
		}
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if provider.grants.Load() != 1 {
		t.Fatal("replayed account creation")
	}
	checkSSHRevocation(t, api, provider)
}

// checkSSHRevocation exercises revocation through the same retained approval protocol.
func checkSSHRevocation(t *testing.T, api *client.Client, provider *sshAccessProvider) {
	t.Helper()
	var revoked map[string]any
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := api.Request(context.Background(), "POST", "/v1/approvals", map[string]any{"method": "DELETE", "path": "/v1/controller/ssh", "body": map[string]any{}}, &revoked); err != nil {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := api.Request(context.Background(), "POST", "/v1/approvals/"+revoked["id"].(string)+"/execute", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if provider.revocations.Load() != 1 {
		t.Fatal("revocation not dispatched")
	}
}
