package controller

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"hostlier/internal/packageexec"
)

// packageFixture builds a small real executable and stages it through the authenticated HTTP handler.
func packageFixture(t *testing.T, f *humanFixture) (packageexec.InstallRequest, []byte) {
	t.Helper()
	root, err := filepath.EvalSymlinks(f.c.opts.StateDir)
	// Production paths are canonical; macOS temporary directories include a /var alias.
	if err != nil {
		t.Fatal(err)
	}
	f.c.opts.StateDir = root
	binary := filepath.Join(t.TempDir(), "native-package")
	command := exec.Command("go", "build", "-o", binary, "../packageexec/testdata/nativefixture")
	// Real native headers exercise the same platform check as release artifacts.
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	data, err := os.ReadFile(binary)
	// Missing fixture output must not look like a rejected upload.
	if err != nil {
		t.Fatal(err)
	}
	request := packageexec.InstallRequest{Name: "postgres", Digest: packageexec.Digest(data), Size: int64(len(data)), Source: packageexec.Source{Repository: "Usefused/hostlier-packages", Version: "v0.1.0", Asset: "hostlier-postgres"}}
	// Staging remains authenticated preparation and never activates content.
	for offset := 0; offset < len(data); offset += packageexec.ChunkBytes {
		end := min(offset+packageexec.ChunkBytes, len(data))
		status, result := humanHTTP(t, f.c, false, "POST", "/v1/package-uploads/"+request.Digest+"/chunks", map[string]any{"offset": offset, "data": data[offset:end]}, f.token, "")
		// Every acknowledged chunk must pass through the production route.
		if status != 200 {
			t.Fatal(status, result)
		}
	}
	return request, data
}

// TestRemotePackageInstallRequiresExactHumanApproval exercises genuine approval signatures and activation receipts.
func TestRemotePackageInstallRequiresExactHumanApproval(t *testing.T) {
	f := newHumanFixture(t)
	request, data := packageFixture(t, f)
	status, _ := humanHTTP(t, f.c, false, "POST", "/v1/packages", request, f.token, "")
	// Bearer authentication alone cannot install arbitrary code under controller privileges.
	if status != 403 {
		t.Fatal("installation bypass", status)
	}
	target := filepath.Join(f.c.opts.StateDir, "packages", request.Name)
	// Merely staging and attempting a direct mutation must not create an executable.
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("unapproved package exists", err)
	}
	approval := f.propose(t, "POST", "/v1/packages", request)
	checkPackageApproval(t, f, approval, request, data, target)
}

// checkPackageApproval verifies displayed integrity, genuine signed execution and idempotent response replay.
func checkPackageApproval(t *testing.T, f *humanFixture, approval map[string]any, request packageexec.InstallRequest, data []byte, target string) {
	t.Helper()
	raw, _ := json.Marshal(approval["review"])
	// The hardware-key owner must see the exact checksum and repository provenance being authorized.
	if !bytes.Contains(raw, []byte(request.Digest)) || !bytes.Contains(raw, []byte(request.Source.Repository)) {
		t.Fatal(string(raw))
	}
	status, result := approveFixtureRequest(t, f, approval)
	// A valid independently signed request activates only its reviewed bytes.
	if status != 200 || result["digest"] != request.Digest {
		t.Fatal(status, result)
	}
	installed, err := os.ReadFile(target)
	// Activation must publish the verified executable without partial copies.
	if err != nil || !bytes.Equal(installed, data) {
		t.Fatal(err)
	}
	generation := f.c.state.Generation
	status, result = humanHTTP(t, f.c, false, "POST", "/v1/approvals/"+approval["id"].(string)+"/execute", map[string]any{}, f.token, "")
	// A lost response must replay a receipt without reinstalling or invalidating state again.
	if status != 200 || result["digest"] != request.Digest || generation != f.c.state.Generation {
		t.Fatal(status, result)
	}
}

// TestRemotePackageRejectsOtherActorAndUnstagedIntent prevents a client from approving another client's upload.
func TestRemotePackageRejectsOtherActorAndUnstagedIntent(t *testing.T) {
	f := newHumanFixture(t)
	request, _ := packageFixture(t, f)
	other := testCredentials(f.c, "other")["token"].(string)
	status, result := humanHTTP(t, f.c, false, "POST", "/v1/approvals", map[string]any{"method": "POST", "path": "/v1/packages", "body": request}, other, "")
	// Source bytes belong to the uploader even when the target name and digest are known.
	if status == 200 {
		t.Fatal("cross-actor package approval", result)
	}
	request.Digest = packageexec.Digest([]byte("unknown"))
	status, result = humanHTTP(t, f.c, false, "POST", "/v1/approvals", map[string]any{"method": "POST", "path": "/v1/packages", "body": request}, f.token, "")
	// Approval cannot reserve an executable identity before its bytes are verified.
	if status == 200 {
		t.Fatal("unstaged package approved", result)
	}
}

// TestRemotePackageRechecksBytesAfterApproval prevents activation when staging changes after review.
func TestRemotePackageRechecksBytesAfterApproval(t *testing.T) {
	f := newHumanFixture(t)
	request, data := packageFixture(t, f)
	approval := f.propose(t, "POST", "/v1/packages", request)
	entries, err := os.ReadDir(f.c.packageUploads().Root)
	// The isolated controller should retain exactly this actor's uploaded artifact.
	if err != nil || len(entries) != 1 {
		t.Fatal(err, len(entries))
	}
	data[len(data)-1] ^= 1
	// Same-size corruption cannot be detected by trusting retained metadata alone.
	if err = os.WriteFile(filepath.Join(f.c.packageUploads().Root, entries[0].Name()), data, 0600); err != nil {
		t.Fatal(err)
	}
	status, result := approveFixtureRequest(t, f, approval)
	// Even a genuine owner signature cannot authorize different bytes from those reviewed.
	if status == 200 {
		t.Fatal("corrupt staged package activated", result)
	}
	// A rejected activation must leave the runnable directory untouched.
	if _, err = os.Stat(filepath.Join(f.c.opts.StateDir, "packages", request.Name)); !os.IsNotExist(err) {
		t.Fatal("corrupt executable exists", err)
	}
}
