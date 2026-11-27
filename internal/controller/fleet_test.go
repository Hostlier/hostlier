package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hostlier/internal/fleet"
)

// approveFixtureRequest supplies a real independent signature and executes the retained request.
func approveFixtureRequest(t *testing.T, f *humanFixture, a map[string]any) (int, map[string]any) {
	t.Helper()
	start := f.beginDecision(t, a, "approve")
	ceremony := f.session(t, start)
	proof := map[string]any{"ceremony": start["ceremony"], "signature": f.signature(t, ceremony.Message, 0x05)}
	status, result := humanHTTP(t, f.c, false, "POST", "/v1/human/decision/finish", proof, "", "")
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, result)
	}
	return humanHTTP(t, f.c, false, "POST", "/v1/approvals/"+a["id"].(string)+"/execute", map[string]any{}, f.token, "")
}

// enrolledHostFixture starts a separate mutually authenticated host and approves its exact enrollment.
func enrolledHostFixture(t *testing.T, f *humanFixture, name string) (*Controller, *httptest.Server) {
	t.Helper()
	host := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	// Keep this callback bounded to the current request and its captured state.
	t.Cleanup(func() { closeController(t, host) })
	server := fleetMasterFixture(t, f.c)
	hostPin, err := host.FleetFingerprint()
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	a := f.propose(t, "POST", "/v1/hosts", map[string]string{"name": name, "url": "https://unreachable.invalid:7464", "fingerprint": hostPin})
	status, result := approveFixtureRequest(t, f, a)
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, result)
	}
	connectFleetFixture(t, host, f.c, server.URL)
	return host, server
}

// TestFleetApprovalTargetsOneEnrolledHost proves real mTLS execution cannot silently select another host.
func TestFleetApprovalTargetsOneEnrolledHost(t *testing.T) {
	f := newHumanFixture(t)
	first, _ := enrolledHostFixture(t, f, "first")
	second, _ := enrolledHostFixture(t, f, "second")
	body := map[string]string{"value": "private-value", "requestId": "fleet-secret"}
	path := "/v1/hosts/first/proxy/secrets/key"
	status, _ := humanHTTP(t, f.c, false, "PUT", path, body, f.token, "")
	// Fail if the fixture violates this invariant: direct bearer mutation.
	if status != 403 {
		t.Fatal("direct bearer mutation", status)
	}
	a := f.propose(t, "PUT", path, body)
	status, result := approveFixtureRequest(t, f, a)
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, result)
	}
	// Fail if the fixture violates this invariant: mutation escaped selected host.
	if first.state.Secrets["key"] == nil || second.state.Secrets["key"] != nil || f.c.state.Secrets["key"] != nil {
		t.Fatal("mutation escaped selected host")
	}
}

// TestFleetRejectsHostChangesAfterReview makes the host enforce its own reviewed generation.
func TestFleetRejectsHostChangesAfterReview(t *testing.T) {
	f := newHumanFixture(t)
	host, _ := enrolledHostFixture(t, f, "worker")
	body := map[string]string{"value": "value", "requestId": "stale-fleet-secret"}
	a := f.propose(t, "PUT", "/v1/hosts/worker/proxy/secrets/key", body)
	host.mu.Lock()
	host.state.Generation++
	host.mu.Unlock()
	status, result := approveFixtureRequest(t, f, a)
	// Fail if the fixture violates this invariant: stale host approval executed.
	if status == 200 || host.state.Secrets["key"] != nil {
		t.Fatal("stale host approval executed", status, result)
	}
}

// TestFleetPinsBothPeers rejects bearer credentials on the mutually authenticated host listener.
func TestFleetPinsBothPeers(t *testing.T) {
	f := newHumanFixture(t)
	_, server := enrolledHostFixture(t, f, "worker")
	// The test server client trusts the server certificate but has no enrolled controller key.
	response, err := server.Client().Get(server.URL + "/v1/host-state")
	// Fail if the fixture violates this invariant: host accepted a client without the controller certificate.
	if err == nil {
		response.Body.Close()
		t.Fatal("host accepted a client without the controller certificate")
	}
	f.c.mu.Lock()
	record := f.c.state.Hosts["worker"]
	record.Fingerprint = random()
	f.c.state.Hosts["worker"] = record
	f.c.mu.Unlock()
	status, result := humanHTTP(t, f.c, false, "GET", "/v1/hosts/worker/proxy/host-state", nil, f.token, "")
	// Fail if the fixture violates this invariant: central controller accepted an unpinned host.
	if status == 200 {
		t.Fatal("central controller accepted an unpinned host", result)
	}
}

// TestFleetHidesAnotherActorsPlans verifies mTLS delegation preserves individual agent ownership.
func TestFleetHidesAnotherActorsPlans(t *testing.T) {
	f := newHumanFixture(t)
	host, _ := enrolledHostFixture(t, f, "worker")
	owner := "controller:" + strings.TrimPrefix(f.c.store.Authenticate("fleet-actor|"+"someone-else"), "hmac:")
	host.mu.Lock()
	host.state.Plans["private"] = &Plan{ID: "private", Actor: owner}
	host.mu.Unlock()
	status, _ := humanHTTP(t, f.c, false, "GET", "/v1/hosts/worker/proxy/plans/private", nil, f.token, "")
	// Fail if the fixture violates this invariant: cross-actor plan disclosure.
	if status != 404 {
		t.Fatal("cross-actor plan disclosure", status)
	}
	status, _ = humanHTTP(t, f.c, false, "GET", "/v1/hosts/worker/proxy/hosts/identity", nil, f.token, "")
	// Fail if the fixture violates this invariant: nested trust administration exposed.
	if status != 404 {
		t.Fatal("nested trust administration exposed", status)
	}
}

// The member exposes no inbound fleet listener, while local management still works.
func TestInstalledFleetListener(t *testing.T) {
	f := newHumanFixture(t)
	host := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	t.Cleanup(func() { closeController(t, host) })
	if err := host.Listen(ListenOptions{TCP: true}); err != nil {
		t.Fatal(err)
	}
	server := fleetMasterFixture(t, f.c)
	pin, _ := host.FleetFingerprint()
	if code, res := approveFixtureRequest(t, f, f.propose(t, "POST", "/v1/hosts", map[string]string{"name": "box", "url": "https://unreachable.invalid:7464", "fingerprint": pin})); code != 200 {
		t.Fatal(code, res)
	}
	connectFleetFixture(t, host, f.c, server.URL)
	response, err := http.Get("http://" + host.Address() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	status, res := humanHTTP(t, f.c, false, "GET", "/v1/hosts/box/proxy/host-state", nil, f.token, "")
	if status != 200 || res["generation"] == nil {
		t.Fatal(status, res)
	}
}

func fleetMasterFixture(t *testing.T, master *Controller) *httptest.Server {
	t.Helper()
	master.mu.Lock()
	cfg, err := master.fleetServerTLS()
	master.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(master.fleetStreamHandler())
	server.EnableHTTP2 = true
	server.TLS = cfg
	server.StartTLS()
	t.Cleanup(func() { server.CloseClientConnections(); server.Close() })
	return server
}

func connectFleetFixture(t *testing.T, host, master *Controller, endpoint string) {
	t.Helper()
	pin, err := master.FleetFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	err = host.switchMaster(pin, endpoint)
	host.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFleetFixture(t, host, master)
}

func waitFleetFixture(t *testing.T, host, master *Controller) {
	t.Helper()
	pin, _ := host.FleetFingerprint()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		master.mu.Lock()
		session := master.fleetSessions[pin]
		ready := session != nil && session.conn.CanTakeNewRequest()
		master.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("controller did not connect outbound to master")
}

// Switch over the local socket, revoke an already open session, and restart.
func TestFleetSwitchMasters(t *testing.T) {
	a, b := newHumanFixture(t), newHumanFixture(t)
	as, bs := fleetMasterFixture(t, a.c), fleetMasterFixture(t, b.c)
	apin, _ := a.c.FleetFingerprint()
	bpin, _ := b.c.FleetFingerprint()
	dir := t.TempDir()
	box := newTestController(t, dir, &recordingRuntime{}, time.Now)
	defer func() { closeController(t, box) }()
	bp, _ := box.FleetFingerprint()
	record := map[string]string{"name": "box", "url": "https://unreachable.invalid:7464", "fingerprint": bp}
	for _, f := range []*humanFixture{a, b} {
		if code, res := approveFixtureRequest(t, f, f.propose(t, "POST", "/v1/hosts", record)); code != 200 {
			t.Fatal(code, res)
		}
	}
	connectFleetFixture(t, box, a.c, as.URL)
	probe := func(f *humanFixture, want bool) {
		t.Helper()
		code, res := humanHTTP(t, f.c, false, "GET", "/v1/hosts/box/proxy/host-state", nil, f.token, "")
		if (code == 200) != want {
			t.Fatal(code, res, want)
		}
	}
	probe(a, true)
	probe(b, false)
	change := fleet.MasterChange{Previous: apin, Fingerprint: bpin, URL: bs.URL}
	if code, _ := humanHTTP(t, box, false, "PUT", "/v1/fleet/master", change, "", ""); code == 200 {
		t.Fatal("remote switch accepted")
	}
	if code, res := humanHTTP(t, box, true, "PUT", "/v1/fleet/master", change, "", ""); code != 200 {
		t.Fatal(code, res)
	}
	waitFleetFixture(t, box, b.c)
	probe(a, false)
	probe(b, true)
	if code, _ := humanHTTP(t, box, true, "PUT", "/v1/fleet/master", change, "", ""); code == 200 {
		t.Fatal("stale switch accepted")
	}
	closeController(t, box)
	box = newTestController(t, dir, &recordingRuntime{}, time.Now)
	if err := box.Listen(ListenOptions{TCP: true, MasterFingerprint: apin, MasterURL: as.URL}); err != nil {
		t.Fatal(err)
	}
	waitFleetFixture(t, box, b.c)
	probe(a, false)
	probe(b, true)
}

func TestFleetReconnectAndRevoke(t *testing.T) {
	f := newHumanFixture(t)
	host, server := enrolledHostFixture(t, f, "box")
	server.CloseClientConnections()
	// Wait for the old reverse session to stop before waiting for its replacement.
	pin, _ := host.FleetFingerprint()
	f.c.mu.Lock()
	previous := f.c.fleetSessions[pin]
	f.c.mu.Unlock()
	deadline := time.Now().Add(8 * time.Second)
	for {
		f.c.mu.Lock()
		current := f.c.fleetSessions[pin]
		f.c.mu.Unlock()
		if current != nil && current != previous {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session did not reconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, res := humanHTTP(t, f.c, false, "GET", "/v1/hosts/box/proxy/host-state", nil, f.token, ""); code != 200 {
		t.Fatal(code, res)
	}
	if code, res := approveFixtureRequest(t, f, f.propose(t, "DELETE", "/v1/hosts/box", map[string]any{})); code != 200 {
		t.Fatal(code, res)
	}
	f.c.mu.Lock()
	session := f.c.fleetSessions[pin]
	f.c.mu.Unlock()
	if session != nil {
		t.Fatal("revoked session retained")
	}
	if code, _ := humanHTTP(t, f.c, false, "GET", "/v1/hosts/box/proxy/host-state", nil, f.token, ""); code == 200 {
		t.Fatal("revoked host accessible")
	}
}

// Legacy pin-only state can adopt an endpoint without changing its trusted master.
func TestFleetLegacyPinAdoptsMasterURL(t *testing.T) {
	f := newHumanFixture(t)
	host := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	t.Cleanup(func() { closeController(t, host) })
	server := fleetMasterFixture(t, f.c)
	masterPin, _ := f.c.FleetFingerprint()
	hostPin, _ := host.FleetFingerprint()
	host.mu.Lock()
	host.state.MasterFingerprint = &masterPin
	host.mu.Unlock()
	if code, res := approveFixtureRequest(t, f, f.propose(t, "POST", "/v1/hosts", map[string]string{"name": "box", "fingerprint": hostPin})); code != 200 {
		t.Fatal(code, res)
	}
	if err := host.Listen(ListenOptions{TCP: true, MasterFingerprint: masterPin, MasterURL: server.URL}); err != nil {
		t.Fatal(err)
	}
	waitFleetFixture(t, host, f.c)
	if code, res := humanHTTP(t, f.c, false, "GET", "/v1/hosts/box/proxy/host-state", nil, f.token, ""); code != 200 {
		t.Fatal(code, res)
	}
}
