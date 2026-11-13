package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"hostlier/internal/model"
	"hostlier/internal/sshidentity"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/client"
)

// TestShellSSHStreamsAndQuotes exercises native process IO, remote option parsing and exit status.
func TestShellSSHStreamsAndQuotes(t *testing.T) {
	home := t.TempDir()
	store := client.NewStore(home)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := store.Save("prod", "https://prod.example.com", "device", "token"); err != nil {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := store.SaveSSH("prod", "", client.SSHConnection{Destination: "admin@fixture"}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// The fixture executes only the final remote command, exactly as an SSH server's shell does.
	script := "#!/bin/sh\nfor arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n"
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	var out, stderr bytes.Buffer
	args := []string{"server", "ssh", "--", "printf", "%s\\n", "a b", "$(exit 91)", "'quoted'", "--help", "--server", "other"}
	err := RunWithOptions(context.Background(), args, strings.NewReader(""), &out, &stderr, Options{Home: home})
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil || out.String() != "a b\n$(exit 91)\n'quoted'\n--help\n--server\nother\n" {
		t.Fatal(err, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	err = RunWithOptions(context.Background(), []string{"server", "ssh", "--", "sh", "-c", "cat; printf diagnostic >&2; exit 42"}, strings.NewReader("payload"), &out, &stderr, Options{Home: home})
	var status *SSHExitError
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if !errors.As(err, &status) || status.Code != 42 || out.String() != "payload" || stderr.String() != "diagnostic" {
		t.Fatal(err, out.String(), stderr.String())
	}
}

// TestShellSSHSelectionPreservesFleetTarget ensures a selected box never falls back to its master.
func TestShellSSHSelectionPreservesFleetTarget(t *testing.T) {
	store := client.NewStore(t.TempDir())
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := store.Save("prod", "https://prod.example.com", "device", "token"); err != nil {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := store.SaveSSH("prod", "", client.SSHConnection{Destination: "root@master"}); err != nil {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := store.SaveSSH("prod", "box", client.SSHConnection{Destination: "admin@box"}); err != nil {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := store.UseHost("prod", "box"); err != nil {
		t.Fatal(err)
	}
	// Exercise each independent SSH input or selection case.
	for _, selection := range []string{"", "prod", "prod/box"} {
		s := newSession(context.Background(), arguments{args: []string{"server", "ssh"}, flags: map[string]string{"server": selection}}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, Options{Home: store.Home})
		_, box, connection, err := s.savedSSHConnection()
		want := "admin@box"
		// Assert the SSH fixture outcome before advancing to the next boundary.
		if selection == "prod" {
			want = "root@master"
		}
		// Assert the SSH fixture outcome before advancing to the next boundary.
		if err != nil || connection.Destination != want || !validFixtureBox(selection, box) {
			t.Fatal(selection, connection, err)
		}
	}
}

// TestShellSSHRejectsAmbiguity keeps dangerous destination syntax and dangling flags out of SSH.
func TestShellSSHRejectsAmbiguity(t *testing.T) {
	// Exercise each independent SSH input or selection case.
	for _, destination := range []string{"-oProxyCommand=evil", "user@host;evil", "user@host\nother", "$(evil)"} {
		// Assert the SSH fixture outcome before advancing to the next boundary.
		if err := validateSSHConnection(client.SSHConnection{Destination: destination}); err == nil {
			t.Fatal(destination)
		}
	}
	// Exercise each independent SSH input or selection case.
	for _, args := range [][]string{{"server", "ssh", "--", "--help"}, {"server", "ssh", "--", "echo", "--json"}} {
		p, err := parseInvocation(args)
		// Assert the SSH fixture outcome before advancing to the next boundary.
		if err != nil || p.flags["help"] != "" || p.flags["json"] != "" {
			t.Fatal(p, err)
		}
	}
}

// TestShellPrivateKeyIsReusedAndProtected checks that enrollment never regenerates a saved identity.
func TestShellPrivateKeyIsReusedAndProtected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	first, err := shellPublicKey(path)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil {
		t.Fatal(err)
	}
	second, err := shellPublicKey(path)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil || first != second {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if _, err = shellPublicKey(path); err == nil {
		t.Fatal("accepted a public private-key file")
	}
}

// TestManagedSSHEnrollmentPinsHostKey exercises the real CLI enrollment and saved transport metadata.
func TestManagedSSHEnrollmentPinsHostKey(t *testing.T) {
	home := t.TempDir()
	store := client.NewStore(home)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err := store.Save("prod", "https://prod.example.com", "device", "token"); err != nil {
		t.Fatal(err)
	}
	hostPublic, _, err := ed25519.GenerateKey(rand.Reader)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewPublicKey(hostPublic)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil {
		t.Fatal(err)
	}
	f := managedSSHRequester(hostKey)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if _, err = invoke([]string{"server", "ssh", "enable"}, "", Options{Home: home, Client: f}); err != nil {
		t.Fatal(err)
	}
	p, err := store.List()
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil {
		t.Fatal(err)
	}
	checkManagedSSHFiles(t, p.SSH["prod"])
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if _, err = invoke([]string{"server", "ssh", "revoke"}, "", Options{Home: home, Client: f}); err != nil {
		t.Fatal(err)
	}
	p, err = store.List()
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil || len(p.SSH) != 0 {
		t.Fatal(err, p.SSH)
	}
}

// managedSSHRequester simulates signed-controller replies while retaining real CLI approval handling.
func managedSSHRequester(hostKey ssh.PublicKey) *fakeClient {
	// Keep this simulated response scoped to the SSH fixture.
	f := &fakeClient{reply: func(c call) (any, error) {
		// Assert the SSH fixture outcome before advancing to the next boundary.
		switch c.path {
		// Return only the protocol result selected by this fixture.
		case "/v1/controller/capabilities":
			return map[string]bool{"sshAccess": true}, nil
		// Return only the protocol result selected by this fixture.
		case "/v1/controller/ssh":
			// Assert the SSH fixture outcome before advancing to the next boundary.
			if c.method == "DELETE" {
				return map[string]bool{"revoked": true}, nil
			}
			raw, _ := json.Marshal(c.body)
			var input model.SSHAccessRequest
			// Assert the SSH fixture outcome before advancing to the next boundary.
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, err
			}
			_, fingerprint, err := sshidentity.PublicKey(input.PublicKey)
			// Assert the SSH fixture outcome before advancing to the next boundary.
			if err != nil {
				return nil, err
			}
			return model.SSHAccess{User: sshidentity.UserName("device"), Port: 2222, HostKey: string(ssh.MarshalAuthorizedKey(hostKey)), Fingerprint: fingerprint}, nil
		// Return only the protocol result selected by this fixture.
		default:
			return nil, fmt.Errorf("unexpected endpoint %s", c.path)
		}
	}}
	return f
}

// checkManagedSSHFiles verifies account binding, key privacy and host pinning independently of the requester.
func checkManagedSSHFiles(t *testing.T, connection client.SSHConnection) {
	t.Helper()
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if !connection.Managed || connection.Destination != sshidentity.UserName("device")+"@prod.example.com" || connection.Port != 2222 {
		t.Fatal(connection)
	}
	known, err := os.ReadFile(connection.KnownHostsFile)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil || !strings.HasPrefix(string(known), "[prod.example.com]:2222 ssh-ed25519 ") {
		t.Fatal(err, string(known))
	}
	key, err := os.Stat(connection.IdentityFile)
	// Assert the SSH fixture outcome before advancing to the next boundary.
	if err != nil || key.Mode().Perm() != 0600 {
		t.Fatal(err)
	}
}

// validFixtureBox makes the expected fleet selection explicit in the routing fixture.
func validFixtureBox(selection, box string) bool { return selection == "prod" || box == "box" }
