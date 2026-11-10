package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrationNodeLimitsSurviveInit proves later installer runs retain the reviewed ceiling.
func TestMigrationNodeLimitsSurviveInit(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "k3s-security.json")
	// Check the observed result against the fixture's migration invariant.
	if err := os.WriteFile(file, []byte(`{"version":1,"podPidsLimit":256}`), 0600); err != nil {
		t.Fatal(err)
	}
	unit, err := k3sMigrationFlags(dir, k3sAssets()["/etc/systemd/system/k3s.service"])
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
	// Exercise each fixture case against the same recovery invariant.
	for _, flag := range []string{"--kubelet-arg=pod-max-pids=256", "--kubelet-arg=fail-swap-on=true", "--kubelet-arg=root-dir=/var/lib/kubelet"} {
		// Check the observed result against the fixture's migration invariant.
		if !strings.Contains(unit, flag) {
			t.Fatal("missing node security enforcement", flag)
		}
	}
	// Check the observed result against the fixture's migration invariant.
	if err := os.WriteFile(file, []byte(`{"version":1,"podPidsLimit":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Check the observed result against the fixture's migration invariant.
	if _, err := k3sMigrationFlags(dir, unit); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
