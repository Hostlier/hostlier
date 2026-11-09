package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/controlrpc/controlrpcconnect"
	"hostlier/internal/model"
)

// Exercise the real gRPC client, with verification enabled, over a network
// connection. Count subscriptions at the edge to prove reconnect uses gRPC.
func grpcFixture(t *testing.T, c *Controller, token string) (*client.Client, *httptest.Server, <-chan struct{}) {
	t.Helper()
	reconnected := make(chan struct{})
	var watches atomic.Int32
	handler := c.Handler()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 || r.Header.Get("Content-Type") != "application/grpc" {
			t.Errorf("unexpected transport: %s %s", r.Proto, r.Header.Get("Content-Type"))
		}
		if r.URL.Path == controlrpcconnect.ControllerWatchOperationProcedure && watches.Add(1) == 2 {
			close(reconnected)
		}
		handler.ServeHTTP(w, r)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	api, err := client.NewGRPC(client.Options{URL: server.URL, CAFile: ca, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	return api, server, reconnected
}

// The cut happens after a real log has been delivered but before deployment
// can finish. Only a new authenticated stream can deliver the final outcome.
func TestGRPCOperationSurvivesLostConnection(t *testing.T) {
	runtime := &recordingRuntime{gate: make(chan struct{})}
	c := newTestController(t, t.TempDir(), runtime, nil)
	defer closeController(t, c)
	f := attachHumanFixture(t, c)
	remote, server, resumed := grpcFixture(t, c, f.token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := acceptStreamDeployment(t, remote, ctx, f)
	var events []client.OperationEvent
	var dropped bool
	done := make(chan error, 1)
	go func() {
		done <- remote.WatchOperation(ctx, id, "", func(ev client.OperationEvent) error {
			events = append(events, ev)
			if ev.ID != "" && !dropped {
				dropped = true
				server.CloseClientConnections()
			}
			return nil
		})
	}()
	select {
	case <-resumed:
	case err := <-done:
		close(runtime.gate)
		t.Fatal("watch ended before reconnect", err)
	case <-ctx.Done():
		close(runtime.gate)
		t.Fatal("watch did not reconnect")
	}
	close(runtime.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertStreamDelivery(t, events)
	if runtime.calls != 1 {
		t.Fatalf("runtime executed %d times", runtime.calls)
	}
}

func TestGRPCDeviceProof(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	c.mu.Lock()
	credential := c.credentials("signed")
	c.state.Clients[credential["clientId"].(string)].PublicKey = base64.RawStdEncoding.EncodeToString(pub)
	c.mu.Unlock()
	token := credential["token"].(string)
	remote, _, _ := grpcFixture(t, c, client.EncodeDeviceCredential(token, key))
	ctx := context.Background()
	if err := remote.Request(ctx, "GET", "/v1/apps?includeDeleted=true", nil, nil); err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := remote.Request(ctx, "POST", "/v1/plans", map[string]any{"spec": project()}, &plan); err != nil || plan["id"] == nil {
		t.Fatalf("signed command: %v", err)
	}
	unsigned, _, _ := grpcFixture(t, c, token)
	err := unsigned.Request(ctx, "GET", "/v1/apps", nil, nil)
	var apiError *model.Error
	if !errors.As(err, &apiError) || apiError.Code != "DEVICE_SESSION" {
		t.Fatalf("unsigned bound credential: %v", err)
	}
}

// The central controller authenticates the stream and the agent independently
// pins its master. A same-ID operation on another host must never be observed.
func TestGRPCFleetWatch(t *testing.T) {
	f := newHumanFixture(t)
	host, _ := enrolledHostFixture(t, f, "box")
	host.mu.Lock()
	host.state.Operations["work"] = &Operation{ID: "work", Status: "succeeded"}
	host.log("agent finished", "work", "info")
	host.mu.Unlock()
	remote, _, _ := grpcFixture(t, f.c, f.token)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got []client.OperationEvent
	if err := remote.WatchOperationOn(ctx, "box", "work", "", func(ev client.OperationEvent) error { got = append(got, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Event != "log" || got[1].Event != "operation" {
		t.Fatal(got)
	}
}

// Use the self-contained package shipped in the CLI, including its protobuf
// descriptor, against the Go service with an ordinary exported device credential.
func TestGRPCBundledNodeSDK(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js not installed")
	}
	ver, err := exec.Command(node, "-p", "process.versions.node.split('.')[0]").Output()
	major, parseErr := strconv.Atoi(strings.TrimSpace(string(ver)))
	if err != nil || parseErr != nil || major < 24 {
		t.Skip("the bundled Cloud SDK requires Node.js 24 or newer")
	}
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api, _, _ := pairWithKey(t, c, deploymentScope{}, public, private)
	_, server, _ := grpcFixture(t, c, "")
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	credential, err := json.Marshal(map[string]string{"controllerUrl": server.URL, "credential": client.EncodeDeviceCredential(api.Token, private)})
	if err != nil {
		t.Fatal(err)
	}
	write("credential.json", credential)
	write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	spec := project()
	spec.Image = "example/image@sha256:" + strings.Repeat("b", 64)
	bundle, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	write(".hostlier", bundle)
	module, err := filepath.Abs("../cli/cloud_sdk_assets/index.js")
	if err != nil {
		t.Fatal(err)
	}
	write("run.mjs", []byte(`const {createHostlierCloud} = await import(process.env.TEST_SDK);
const sdk = await createHostlierCloud({bundlePath:'.hostlier', stateDirectory:'state', credentialFile:'credential.json'});
await sdk.create({name:'sample',route:'sample.example.com'});
const status = await sdk.statusSnapshot('sample');
if (status.name !== 'sample' || status.status !== 'running') throw Error('unexpected deployment status');
`))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "run.mjs")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TEST_SDK="+module, "NODE_EXTRA_CA_CERTS="+filepath.Join(dir, "ca.pem"), "GRPC_DEFAULT_SSL_ROOTS_FILE_PATH="+filepath.Join(dir, "ca.pem"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bundled SDK lifecycle: %v: %s", err, out)
	}
}

func TestGRPCApprovalWatch(t *testing.T) {
	f := newHumanFixture(t)
	approval := f.propose(t, "PUT", "/v1/secrets/key", map[string]string{"requestId": "one", "value": "protected"})
	remote, _, _ := grpcFixture(t, f.c, f.token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path := "/v1/approvals/" + approval["id"].(string)
	states := make(chan string, 3)
	done := make(chan error, 1)
	go func() {
		done <- remote.WatchJob(ctx, "", path, func(raw json.RawMessage) error {
			var state receiptState
			if err := json.Unmarshal(raw, &state); err != nil {
				return err
			}
			states <- state.Status
			return nil
		})
	}()
	select {
	case state := <-states:
		if state != "pending" {
			t.Fatal(state)
		}
	case err := <-done:
		t.Fatal("watch did not deliver pending approval", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// An independent signer rejects the proposal. Only the watch may supply the result.
	start := f.beginDecision(t, approval, "reject")
	ceremony := f.session(t, start)
	proof := map[string]any{"ceremony": start["ceremony"], "signature": f.signature(t, ceremony.Message, 0x05)}
	status, result := humanHTTP(t, f.c, false, "POST", "/v1/human/decision/finish", proof, "", "")
	if status != 200 {
		t.Fatal(status, result)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state := <-states; state != "rejected" {
		t.Fatal(state)
	}
}

func TestGRPCUnixAdministration(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	// Darwin limits sockaddr_un paths to 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "hostlier-grpc-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "api.sock")
	if err := c.Listen(ListenOptions{SocketPath: sock}); err != nil {
		t.Fatal(err)
	}
	local, err := client.NewGRPC(client.Options{SocketPath: sock})
	if err != nil {
		t.Fatal(err)
	}
	var clients map[string]any
	if err := local.Request(context.Background(), "GET", "/v1/clients", nil, &clients); err != nil {
		t.Fatal(err)
	}
	remote, _, _ := grpcFixture(t, c, "")
	var apiErr *model.Error
	if err := remote.Request(context.Background(), "GET", "/v1/clients", nil, &clients); !errors.As(err, &apiErr) || apiErr.Code != "UNAUTHORIZED" {
		t.Fatalf("remote caller obtained local authority: %v", err)
	}
}
