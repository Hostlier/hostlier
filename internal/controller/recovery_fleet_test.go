package controller

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// enrollRecoveryHost connects the native-data fixture through the production pinned mutual-TLS listener.
func enrollRecoveryHost(t *testing.T, f *humanFixture, host *Controller) {
	t.Helper()
	pin, err := f.c.FleetFingerprint()
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal(err)
	}
	options := ListenOptions{ControllerFingerprint: pin}
	host.mu.Lock()
	err = host.prepareAgentListener(&options)
	host.mu.Unlock()
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(host.remoteListenerHandler(options))
	server.TLS = options.TLSConfig
	server.StartTLS()
	t.Cleanup(server.Close)
	hostPin, err := host.FleetFingerprint()
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal(err)
	}
	recoveryApproved(t, f, "/v1/hosts", map[string]string{"name": "data", "url": server.URL, "fingerprint": hostPin})
}

// TestRecoveryFleetSchedulingKeepsRevocationCentral prevents remote agents from retaining independent recurring approval.
func TestRecoveryFleetSchedulingKeepsRevocationCentral(t *testing.T) {
	central := newHumanFixture(t)
	host, _, _, destination := recoveryFixture(t)
	destination.Endpoint = "https://remote-storage.example"
	enrollRecoveryHost(t, central, host.c)
	body := map[string]any{"name": "daily", "resource": "db", "destination": "offsite", "destinationFingerprint": destination.Fingerprint(), "everySeconds": 3600, "retain": 2}
	proposed := central.propose(t, "POST", "/v1/hosts/data/proxy/backup-policies", body)
	review, _ := json.Marshal(proposed)
	// Fail the fixture if the recovery invariant is violated.
	if !bytes.Contains(review, []byte("remote-storage.example")) {
		t.Fatal("review omitted host-authoritative destination")
	}
	status, result := approveFixtureRequest(t, central, proposed)
	// Fail the fixture if the recovery invariant is violated.
	if status != 200 {
		t.Fatal(status, result)
	}
	host.c.mu.Lock()
	host.c.state.BackupPolicies["daily"].NextRun = 0
	host.c.mu.Unlock()
	host.c.scheduleBackups()
	assertRecoveryCount(t, host.c, 0)
	central.c.scheduleBackups()
	assertRecoveryCount(t, host.c, 1)
	host.c.mu.Lock()
	id := ""
	// Exercise each native recovery case independently.
	for key := range host.c.state.RecoveryJobs {
		id = key
	}
	host.c.mu.Unlock()
	waitRecovery(t, host.c, id, "succeeded")
	revokeRemoteSchedule(t, central.c)
	host.c.mu.Lock()
	host.c.state.BackupPolicies["daily"].NextRun = 0
	host.c.mu.Unlock()
	host.c.scheduleBackups()
	central.c.scheduleBackups()
	assertRecoveryCount(t, host.c, 1)
}

// assertRecoveryCount observes job acceptance under the same state lock used by scheduling.
func assertRecoveryCount(t *testing.T, c *Controller, want int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	// Fail the fixture if the recovery invariant is violated.
	if len(c.state.RecoveryJobs) != want {
		t.Fatalf("accepted jobs=%d want=%d", len(c.state.RecoveryJobs), want)
	}
}

// revokeRemoteSchedule changes only the central human-key record, leaving the host unreachable from that administration action.
func revokeRemoteSchedule(t *testing.T, c *Controller) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	schedule, ok := c.state.RemoteBackupSchedules["data/daily"]
	// Fail the fixture if the recovery invariant is violated.
	if !ok || schedule.KeyFingerprint == "" {
		t.Fatal("missing central hardware-key provenance")
	}
	key := c.state.Human.Keys[schedule.KeyFingerprint]
	key.Revoked = true
	c.state.Human.Keys[schedule.KeyFingerprint] = key
}
