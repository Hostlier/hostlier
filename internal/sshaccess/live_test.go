package sshaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"hostlier/internal/model"
)

// TestManagedSSHLive runs only inside the explicitly marked disposable SSH test container.
func TestManagedSSHLive(t *testing.T) {
	requireSSHTestContainer(t)
	ctx := context.Background()
	p, access, public, args := liveSSHFixture(t)
	probe := `set -eu; test "$(id -u)" != 0; test "$(id -Gn)" = "$(id -un)"; test ! -w "$HOME"; test ! -w "$HOME/.ssh/authorized_keys"; test -w "$HOME/work"; printf retained > "$HOME/work/probe"; if sudo -n true 2>/dev/null; then exit 70; fi; printf safe`
	out, err := exec.Command("ssh", append(args, probe)...).CombinedOutput()
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil || string(out) != "safe" {
		t.Fatal(err, string(out))
	}
	// Re-grant is idempotent and cannot add supplementary privilege groups.
	if _, err = p.Grant(ctx, "fixture-device", model.SSHAccessRequest{PublicKey: public}); err != nil {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err = p.Revoke(ctx, "fixture-device"); err != nil {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if out, err = exec.Command("ssh", append(args, "true")...).CombinedOutput(); err == nil {
		t.Fatal("revoked key still accepted", string(out))
	}
	data, err := os.ReadFile(filepath.Join(homes, access.User, "work", "probe"))
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil || strings.TrimSpace(string(data)) != "retained" {
		t.Fatal("revocation lost workspace", err)
	}
}

// liveSSHFixture creates a real root-owned account and a matching private test client identity.
func liveSSHFixture(t *testing.T) (*Provider, model.SSHAccess, string, []string) {
	t.Helper()
	ctx := context.Background()
	p := &Provider{StateDir: "/var/lib/hostlier-ssh-test"}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ssh.NewPublicKey(pub)
	public := string(ssh.MarshalAuthorizedKey(key))
	access, err := p.Grant(ctx, "fixture-device", model.SSHAccessRequest{PublicKey: public})
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil {
		t.Fatal(err)
	}
	privatePath := "/root/ssh-fixture-key"
	block, _ := ssh.MarshalPrivateKey(priv, "")
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err = os.WriteFile(privatePath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	known := "/root/ssh-fixture-known"
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err = os.WriteFile(known, []byte("[127.0.0.1]:2222 "+access.HostKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-p", "2222", "-i", privatePath, "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + known, "-o", "BatchMode=yes", access.User + "@127.0.0.1"}
	return p, access, public, args
}

// requireSSHTestContainer prevents the privileged live test from running on an ordinary Linux host.
func requireSSHTestContainer(t *testing.T) {
	t.Helper()
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || os.Getenv("HOSTLIER_SSH_CONTAINER_TEST") != "1" {
		t.Skip("disposable root Linux container only")
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if _, err := os.Stat("/run/hostlier-ssh-test-container"); err != nil {
		t.Fatal("missing disposable-container marker")
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := os.MkdirAll("/var/lib/hostlier-ssh-test", 0700); err != nil {
		t.Fatal(err)
	}
}
