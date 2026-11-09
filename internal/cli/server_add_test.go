package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/client"
	"hostlier/internal/fleet"
)

// Exercise the SSH command and byte stream in a local shell with tiny release fixtures.
func TestServerAddOneSSH(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("BOX", dir)
	master, pin := strings.Repeat("1", 64), strings.Repeat("2", 64)
	write("ssh", `#!/bin/sh
set -eu
echo ssh >> "$BOX/connections"
for arg do remote=$arg; done
# Redirect privileged paths to the fixture and exercise the production bootstrap script.
printf '%s' "$remote" | sed "s|/run/hostlier-server-add.lock|$BOX/lock|g; s|/run/hostlier/controller.sock|$BOX/socket|g" > "$BOX/command"
exec sh "$BOX/command"
`)
	write("id", "#!/bin/sh\necho 1001\n")
	write("sudo", "#!/bin/sh\n[ \"$1 $2\" = \"-n --\" ] || exit 1\nshift 2\nexec \"$@\"\n")
	if _, err := exec.LookPath("sha256sum"); err != nil {
		write("sha256sum", "#!/bin/sh\nexec shasum -a 256 \"$@\"\n")
	}
	write("flock", "#!/bin/sh\nexit 0\n")
	write("uname", "#!/bin/sh\necho aarch64\n")
	write("hostlier-controller", "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$BOX/init-args\"\n")
	write("hostlier", "#!/bin/sh\necho '{\"fingerprint\":\""+pin+"\"}'\n")
	if err := os.Mkdir(filepath.Join(dir, "collector-image"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"controller", "cli"} {
		name := "hostlier-" + kind + "-linux-arm64.tar.gz"
		entries := []string{"hostlier"}
		if kind == "controller" {
			entries = []string{"hostlier-controller", "collector-image"}
		}
		cmd := exec.Command("tar", append([]string{"-czf", name}, entries...)...)
		cmd.Dir = dir
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		write(name+".sha256", fmt.Sprintf("%x  %s\n", sha256.Sum256(data), name))
	}
	hosts := map[string]fleet.HostRecord{}
	f := &fakeClient{reply: func(c call) (any, error) {
		switch c.path {
		case "/v1/hosts/identity":
			return map[string]string{"fingerprint": master}, nil
		case "/v1/hosts":
			if c.method == "GET" {
				return map[string]any{"hosts": hosts}, nil
			}
			r := c.body.(fleet.HostRecord)
			if r != (fleet.HostRecord{Name: "box", URL: "https://box.example.com:7464", Fingerprint: pin}) {
				t.Fatalf("wrong enrollment %+v", r)
			}
			hosts[r.Name] = r
			return r, nil
		case "/v1/hosts/box/proxy/host-state":
			return map[string]int{"generation": 42}, nil
		default:
			t.Fatalf("unexpected request %+v", c)
			return nil, nil
		}
	}}
	args := []string{"server", "add", "admin@box.example.com", "--release-dir", dir}
	store := client.NewStore(t.TempDir())
	if err := store.Save("prod", "https://prod.example.com", "device", "token"); err != nil {
		t.Fatal(err)
	}
	for i, invocation := range [][]string{append([]string{"connect"}, args[2:]...), args} {
		var out, logs bytes.Buffer
		err := RunWithOptions(context.Background(), invocation, strings.NewReader(""), &out, &logs, Options{Client: f, Home: store.Home})
		if err != nil || !strings.Contains(out.String(), pin) {
			t.Fatalf("run %d: %s %s %v", i, out.String(), logs.String(), err)
		}
	}
	profiles, _ := store.List()
	if profiles.Current != "prod" || profiles.CurrentHost != "box" {
		t.Fatal(profiles)
	}
	connections, _ := os.ReadFile(filepath.Join(dir, "connections"))
	if string(connections) != "ssh\n" {
		t.Fatalf("connections: %q", connections)
	}
	init, _ := os.ReadFile(filepath.Join(dir, "init-args"))
	if string(init) != "init\n--yes\n--domain\nbox.example.com\n--controller-hostname\nbox.example.com\n--master-fingerprint\n"+master+"\n" {
		t.Fatalf("init args: %q", init)
	}
	// A corrupt release must never reach init or request enrollment.
	clear(hosts)
	write("init-args", "")
	write("hostlier-cli-linux-arm64.tar.gz", "corrupt")
	if _, err := invoke(args, "", Options{Client: f}); err == nil || len(hosts) != 0 {
		t.Fatalf("corrupt release enrolled: %v", err)
	}
	init, _ = os.ReadFile(filepath.Join(dir, "init-args"))
	if len(init) != 0 {
		t.Fatal("corrupt release ran installer")
	}
}

func TestServerAddRejectsInvalidTargetsBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{{"server", "add", "--", "bad"}, {"server", "add", "-oProxyCommand=evil"}, {"server", "add", "user@box.example.com", "extra"}, {"server", "add", "admin@box.example.com", "--name", "local"}, {"server", "add", "user@127.0.0.1", "--domain", "invalid"}} {
		f := &fakeClient{}
		if _, err := invoke(args, "", Options{Client: f}); err == nil || len(f.calls) > 0 {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

// CI clients fetch only the named architecture from successful trusted workflow events.
func TestServerAddBuildMatchedArtifacts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("GH_CALLS", filepath.Join(dir, "calls"))
	oldRepo, oldCommit := serverArtifactRepository, serverArtifactCommit
	t.Cleanup(func() { serverArtifactRepository, serverArtifactCommit = oldRepo, oldCommit })
	serverArtifactRepository, serverArtifactCommit = "example/hostlier", strings.Repeat("a", 40)
	stub := `#!/bin/sh
echo "$@" >> "$GH_CALLS"
if [ "$2" = list ]; then echo '[{"databaseId":1,"event":"pull_request"},{"databaseId":2,"event":"push"}]'; else test "$3" = 2; fi
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	s := session{errOut: &bytes.Buffer{}}
	stage, cleanup, err := s.serverReleases(context.Background(), dir, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("download staging was retained")
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"cli", "controller"} {
		if !strings.Contains(string(calls), "--workflow hostlier-"+kind+".yml --branch main --commit "+serverArtifactCommit+" --status success") || !strings.Contains(string(calls), "download 2 --repo example/hostlier --name hostlier-"+kind+"-linux-amd64") {
			t.Fatal(string(calls))
		}
	}
}
