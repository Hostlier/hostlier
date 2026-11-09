package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBootstrapEffectiveSSHPolicy checks native SSH's interpretation without connecting to a host.
func TestBootstrapEffectiveSSHPolicy(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	// Hosts without OpenSSH cannot exercise native configuration precedence.
	if err != nil {
		t.Skip("OpenSSH is unavailable")
	}
	config := filepath.Join(t.TempDir(), "config")
	// Conflicting user defaults must not silently disable verification or required interactive consent.
	if err = os.WriteFile(config, []byte("Host *\n  StrictHostKeyChecking no\n  BatchMode yes\n  ForwardAgent yes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &session{serverTarget: "https://controller.example.com", flags: map[string]string{"ssh": "root@127.0.0.1", "ssh-config": config}}
	args := append([]string{"-G"}, s.bootstrapSSHArguments("pending")...)
	output, err := exec.Command(ssh, args...).Output()
	// -G resolves configuration only; it must never require live network access or change known_hosts.
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{}
	// Parse effective settings rather than asserting only the argument string we constructed.
	for _, line := range strings.Split(string(output), "\n") {
		name, value, ok := strings.Cut(line, " ")
		// OpenSSH emits one setting per line, with optional platform-specific extras.
		if ok {
			fields[name] = value
		}
	}
	// New keys need explicit consent, prompts remain available, and private agent access is not forwarded.
	if fields["stricthostkeychecking"] != "ask" || fields["batchmode"] != "no" || fields["forwardagent"] != "no" {
		t.Fatal("unexpected native SSH policy", fields["stricthostkeychecking"], fields["batchmode"], fields["forwardagent"])
	}
}
