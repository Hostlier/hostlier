package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/fleet"
	"hostlier/internal/fleetjoin"
)

// prepareJoinFixture exercises the member's protected signing endpoint.
func prepareJoinFixture(t *testing.T, member *Controller, name string) fleet.PreparedJoin {
	t.Helper()
	code, result := humanHTTP(t, member, true, "POST", "/v1/fleet-joins/prepare", fleet.JoinPrepare{URL: "https://hostlier.example.com", Name: name}, "", "")
	if code != 200 {
		t.Fatal(code, result)
	}
	data, _ := json.Marshal(result)
	var prepared fleet.PreparedJoin
	if err := json.Unmarshal(data, &prepared); err != nil {
		t.Fatal(err)
	}
	return prepared
}

// TestFleetJoinTrustBoundaries covers signed identity, capability secrecy and local-only approval.
func TestFleetJoinTrustBoundaries(t *testing.T) {
	master := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	member := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, master)
	defer closeController(t, member)
	prepared := prepareJoinFixture(t, member, "worker")
	request := prepared.Request
	tampered := request
	tampered.Name = "attacker"
	if code, _ := humanHTTP(t, master, false, "POST", "/v1/fleet-joins/request", tampered, "", ""); code == 200 {
		t.Fatal("altered identity accepted")
	}
	code, result := humanHTTP(t, master, false, "POST", "/v1/fleet-joins/request", request, "", "")
	if code != 200 {
		t.Fatal(code, result)
	}
	joinCode := result["code"].(string)
	pin := result["fingerprint"].(string)
	if code, _ := humanHTTP(t, master, false, "POST", "/v1/fleet-joins/poll", fleet.JoinPoll{Code: joinCode, Token: request.TokenHash}, "", ""); code == 200 {
		t.Fatal("public hash accepted as private capability")
	}
	path := "/v1/fleet-joins/" + joinCode
	if code, _ := humanHTTP(t, master, false, "POST", path+"/approve", fleet.JoinDecision{Fingerprint: pin}, "", ""); code == 200 {
		t.Fatal("remote approval accepted")
	}
	if code, _ := humanHTTP(t, master, true, "POST", path+"/approve", fleet.JoinDecision{Fingerprint: strings.Repeat("a", 64)}, "", ""); code == 200 {
		t.Fatal("unreviewed identity approved")
	}
	if code, result := humanHTTP(t, master, true, "POST", path+"/approve", fleet.JoinDecision{Fingerprint: pin}, "", ""); code != 200 {
		t.Fatal(code, result)
	}
	if code, _ := humanHTTP(t, master, true, "POST", path+"/approve", fleet.JoinDecision{Fingerprint: pin}, "", ""); code == 200 {
		t.Fatal("approval reused")
	}
	if code, result := humanHTTP(t, master, false, "POST", "/v1/fleet-joins/poll", fleet.JoinPoll{Code: joinCode, Token: prepared.Token}, "", ""); code != 200 || result["approved"] != true {
		t.Fatal(code, result)
	}
	master.mu.Lock()
	if len(master.state.Clients) != 0 {
		t.Error("join issued administrator credentials")
	}
	delete(master.state.Hosts, "worker")
	master.mu.Unlock()
	if code, _ := humanHTTP(t, master, false, "POST", "/v1/fleet-joins/poll", fleet.JoinPoll{Code: joinCode, Token: prepared.Token}, "", ""); code == 200 {
		t.Fatal("revoked enrollment returned as approved")
	}
}

// TestFleetJoinExpiryAndRestart ensures pending requests survive restart but never gain extra lifetime.
func TestFleetJoinExpiryAndRestart(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	master := newTestController(t, dir, &recordingRuntime{}, func() time.Time { return now })
	member := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, member)
	prepared := prepareJoinFixture(t, member, "worker")
	code, result := humanHTTP(t, master, false, "POST", "/v1/fleet-joins/request", prepared.Request, "", "")
	if code != 200 {
		t.Fatal(code, result)
	}
	joinCode := result["code"].(string)
	closeController(t, master)
	master = newTestController(t, dir, &recordingRuntime{}, func() time.Time { return now })
	defer closeController(t, master)
	if code, _ := humanHTTP(t, master, true, "GET", "/v1/fleet-joins/"+joinCode, nil, "", ""); code != 200 {
		t.Fatal("pending join lost on restart")
	}
	master.mu.Lock()
	master.state.FleetJoins[joinCode].Request.ExpiresAt = now.Add(-time.Second).UnixMilli()
	master.mu.Unlock()
	if code, _ := humanHTTP(t, master, true, "POST", "/v1/fleet-joins/"+joinCode+"/approve", fleet.JoinDecision{Fingerprint: result["fingerprint"].(string)}, "", ""); code == 200 {
		t.Fatal("expired request approved")
	}
}

// joinOutput provides deterministic approval coordination without races on terminal output.
type joinOutput struct {
	mu sync.Mutex
	bytes.Buffer
	code chan string
}

func (w *joinOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, line := range strings.Split(string(data), "\n") {
		if code, ok := strings.CutPrefix(line, "Approval code: "); ok {
			w.code <- code
		}
	}
	return w.Buffer.Write(data)
}

// TestFleetJoinCLIOnlyEndToEnd uses native gRPC, real sockets and the reverse fleet session.
func TestFleetJoinCLIOnlyEndToEnd(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hl-join-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	master := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	member := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, master)
	defer closeController(t, member)
	master.mu.Lock()
	master.state.Hostname = "127.0.0.1"
	master.mu.Unlock()
	masterSocket, memberSocket := filepath.Join(dir, "master.sock"), filepath.Join(dir, "member.sock")
	if err := master.Listen(ListenOptions{SocketPath: masterSocket, FleetAddress: "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	if err := member.Listen(ListenOptions{SocketPath: memberSocket}); err != nil {
		t.Fatal(err)
	}
	public := httptest.NewUnstartedServer(master.Handler())
	public.EnableHTTP2 = true
	public.StartTLS()
	defer public.Close()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: public.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	admin, err := client.NewGRPC(client.Options{SocketPath: masterSocket})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.HTTP.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output := &joinOutput{code: make(chan string, 1)}
	done := make(chan error, 1)
	go func() {
		done <- fleetjoin.RunConnect(ctx, []string{public.URL, "--name", "worker", "--socket", memberSocket, "--ca-file", ca}, output, io.Discard)
	}()
	var code string
	select {
	case code = <-output.code:
	case err := <-done:
		t.Fatal("join failed before approval", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := fleetjoin.Approve(ctx, admin, code, strings.NewReader("yes\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := admin.Request(ctx, "GET", "/v1/hosts/worker/proxy/host-state", nil, &state); err != nil {
		t.Fatal(err)
	}
	if state["generation"] == nil {
		t.Fatal(state)
	}
	member.mu.Lock()
	pin := member.master.peer.pin
	endpoint := member.state.MasterURL
	member.mu.Unlock()
	expected, _ := master.FleetFingerprint()
	if pin != expected || endpoint == "" {
		t.Fatal("master identity not saved", pin, endpoint)
	}
	output.mu.Lock()
	text := output.String()
	output.mu.Unlock()
	if !strings.Contains(text, "connected to") {
		t.Fatal(text)
	}
}
