package controller

import (
	"io"
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
	pin, err := f.c.FleetFingerprint()
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	options := ListenOptions{ControllerFingerprint: pin}
	host.mu.Lock()
	err = host.prepareAgentListener(&options)
	host.mu.Unlock()
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(host.remoteListenerHandler(options))
	server.EnableHTTP2 = true
	server.TLS = options.TLSConfig
	server.StartTLS()
	t.Cleanup(server.Close)
	hostPin, err := host.FleetFingerprint()
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	a := f.propose(t, "POST", "/v1/hosts", map[string]string{"name": name, "url": server.URL, "fingerprint": hostPin})
	status, result := approveFixtureRequest(t, f, a)
	// Stop when fixture setup or the exercised operation fails.
	if status != 200 {
		t.Fatal(status, result)
	}
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

// The normal loopback and pinned fleet listeners operate on the same per-box controller.
func TestInstalledFleetListener(t *testing.T) {
	f := newHumanFixture(t)
	host := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	t.Cleanup(func() { closeController(t, host) })
	masterPin, err := f.c.FleetFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Listen(ListenOptions{TCP: true, MasterFingerprint: masterPin}); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get("http://" + host.Address() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("local edge status %d", response.StatusCode)
	}
	pin, err := host.FleetFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	record := map[string]string{"name": "box", "url": "https://127.0.0.1:" + fleet.Port, "fingerprint": pin}
	a := f.propose(t, "POST", "/v1/hosts", record)
	if code, res := approveFixtureRequest(t, f, a); code != 200 {
		t.Fatal(code, res)
	}
	status, res := humanHTTP(t, f.c, false, "GET", "/v1/hosts/box/proxy/host-state", nil, f.token, "")
	if status != 200 || res["generation"] == nil {
		t.Fatal(status, res)
	}
}

// Switch via the protected local interface and restart with the original installer flag.
func TestFleetSwitchMasters(t *testing.T) {
	a, b := newHumanFixture(t), newHumanFixture(t)
	apin, err := a.c.FleetFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	bpin, err := b.c.FleetFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	start := func(bootstrap string) *Controller {
		box := newTestController(t, dir, &recordingRuntime{}, time.Now)
		if err := box.Listen(ListenOptions{TCP: true, MasterFingerprint: bootstrap}); err != nil {
			t.Fatal(err)
		}
		return box
	}
	box := start("")
	defer func() { closeController(t, box) }()
	if code, res := humanHTTP(t, box, true, "PUT", "/v1/fleet/master", fleet.MasterChange{Fingerprint: apin}, "", ""); code != 200 {
		t.Fatal(code, res)
	}
	bp, err := box.FleetFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	record := map[string]string{"name": "box", "url": "https://127.0.0.1:" + fleet.Port, "fingerprint": bp}
	for _, master := range []*humanFixture{a, b} {
		if code, res := approveFixtureRequest(t, master, master.propose(t, "POST", "/v1/hosts", record)); code != 200 {
			t.Fatal(code, res)
		}
	}
	probe := func(f *humanFixture, want bool) {
		t.Helper()
		code, _ := humanHTTP(t, f.c, false, "GET", "/v1/hosts/box/proxy/host-state", nil, f.token, "")
		if (code == 200) != want {
			t.Fatalf("probe status %d, expected success=%v", code, want)
		}
	}
	a.c.mu.Lock()
	identity, err := a.c.fleetIdentity()
	a.c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := fleet.TLS(identity, bp, false)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	defer transport.CloseIdleConnections()
	oldClient := http.Client{Transport: transport}
	oldRequest := func() (int, error) {
		req, err := http.NewRequest("GET", record["url"]+"/v1/host-state", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Hostlier-Actor", strings.Repeat("a", 64))
		resp, err := oldClient.Do(req)
		if err != nil {
			return 0, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	if got, err := oldRequest(); err != nil || got != http.StatusOK {
		t.Fatal(got, err)
	}
	probe(a, true)
	probe(b, false)
	change := fleet.MasterChange{Previous: apin, Fingerprint: bpin}
	if code, _ := humanHTTP(t, box, false, "PUT", "/v1/fleet/master", change, "", ""); code == 200 {
		t.Fatal("remote switch accepted")
	}
	if code, res := humanHTTP(t, box, true, "PUT", "/v1/fleet/master", change, "", ""); code != 200 {
		t.Fatal(code, res)
	}
	if got, err := oldRequest(); got != http.StatusForbidden && (err == nil || !strings.Contains(err.Error(), "fleet peer identity mismatch") && !strings.Contains(err.Error(), "tls: bad certificate")) {
		t.Fatal("old TLS connection retained access", got, err)
	}
	probe(a, false)
	probe(b, true)
	if code, _ := humanHTTP(t, box, true, "PUT", "/v1/fleet/master", change, "", ""); code == 200 {
		t.Fatal("stale switch accepted")
	}
	closeController(t, box)
	box = start(apin)
	probe(a, false)
	probe(b, true)
}
