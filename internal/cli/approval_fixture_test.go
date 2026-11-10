package cli

import (
	"bytes"
	"connectrpc.com/connect"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	pb "hostlier/internal/controlrpc"
	"hostlier/internal/controlrpc/controlrpcconnect"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"hostlier/internal/controller"
	"hostlier/internal/securitykey"
)

// fixtureHuman is an independent software authenticator used only by CLI integration tests.
type fixtureHuman struct {
	mu         sync.Mutex
	key        *ecdsa.PrivateKey
	public     ssh.PublicKey
	counter    uint32
	controller *controller.Controller
	handler    http.Handler
}

// withFixtureHuman enrolls test-only authority and signs server-retained requests through the real verification API.
func withFixtureHuman(t *testing.T, c *controller.Controller, handler http.Handler) http.Handler {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.ParsePublicKey(ssh.Marshal(struct {
		Type, Curve string
		Point       []byte
		Application string
	}{ssh.KeyAlgoSKECDSA256, "nistp256", elliptic.Marshal(key.Curve, key.X, key.Y), "ssh:hostlier"}))
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	f := &fixtureHuman{key: key, public: public, controller: c, handler: handler}
	out := fixtureHTTP(c.LocalHandler(), "POST", "/v1/human/enroll", map[string]string{"name": "CLI test human", "publicKey": string(ssh.MarshalAuthorizedKey(public))}, nil)
	// Select the fixture behavior required to exercise this boundary.
	if out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	return f
}

// fixtureHTTP performs one ordinary handler request without altering authorization state directly.
func fixtureHTTP(handler http.Handler, method, path string, input any, headers http.Header) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(input)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	// Select the fixture behavior required to exercise this boundary.
	if headers != nil {
		r.Header = headers.Clone()
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, r)
	return out
}

// ServeHTTP adds independent human decisions only at the test boundary; all controller cryptography still executes.
func (f *fixtureHuman) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Select the fixture behavior required to exercise this boundary.
	if r.Method != "POST" || r.URL.Path != "/v1/approvals" {
		f.handler.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, r)
	var a map[string]any
	json.Unmarshal(response.Body.Bytes(), &a)
	// Stop when fixture setup or the exercised operation fails.
	if response.Code == 200 && a["status"] == "pending" {
		response = f.decide(a, r.Header)
	}
	// Exercise every case independently, including denied inputs.
	for key, values := range response.Header() {
		w.Header()[key] = values
	}
	w.WriteHeader(response.Code)
	io.Copy(w, response.Body)
}

// decide emulates a separate human terminal signing precisely the controller-issued challenge.
func (f *fixtureHuman) decide(a map[string]any, headers http.Header) *httptest.ResponseRecorder {
	begin := fixtureHTTP(f.controller.Handler(), "POST", "/v1/human/decision/begin", map[string]any{"id": a["id"], "fingerprint": a["fingerprint"], "decision": "approve", "keyFingerprint": ssh.FingerprintSHA256(f.public)}, nil)
	// Select the fixture behavior required to exercise this boundary.
	if begin.Code != 200 {
		return begin
	}
	var session struct {
		ID      string `json:"ceremony"`
		Message string `json:"message"`
	}
	json.Unmarshal(begin.Body.Bytes(), &session)
	return fixtureHTTP(f.controller.Handler(), "POST", "/v1/human/decision/finish", map[string]string{"ceremony": session.ID, "signature": f.sign(session.Message)}, nil)
}

// sign constructs a standards-shaped test authenticator proof including signed presence and verification flags.
func (f *fixtureHuman) sign(message string) string {
	f.counter++
	envelope := securitykey.Envelope{Version: 1, PublicKey: f.public.Marshal(), Namespace: securitykey.Namespace, HashAlgorithm: "sha512"}
	data, _ := securitykey.SignedData(envelope, []byte(message))
	application := sha256.Sum256([]byte("ssh:hostlier"))
	digest := sha256.Sum256(data)
	authenticator := append(append([]byte{}, application[:]...), 5, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(authenticator[33:37], f.counter)
	authenticator = append(authenticator, digest[:]...)
	signed := sha256.Sum256(authenticator)
	r, s, err := ecdsa.Sign(rand.Reader, f.key, signed[:])
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		panic(err)
	}
	envelope.Signature = ssh.Marshal(ssh.Signature{Format: ssh.KeyAlgoSKECDSA256, Blob: ssh.Marshal(struct{ R, S *big.Int }{r, s}), Rest: ssh.Marshal(struct {
		Flags   byte
		Counter uint32
	}{5, f.counter})})
	return string(pem.EncodeToMemory(&pem.Block{Type: "SSH SIGNATURE", Bytes: append([]byte("SSHSIG"), ssh.Marshal(envelope)...)}))
}

// approvalSimulation isolates legacy command behavior tests from independently tested controller authorization.
type approvalSimulation struct {
	pending  call
	sequence int
}

// respond returns a simulated independently approved receipt and retains exact intent until execution.
func (s *approvalSimulation) respond(method, path string, body, result any) (bool, call, error) {
	// Select the fixture behavior required to exercise this boundary.
	if method == "POST" && path == "/v1/approvals" {
		request := body.(map[string]any)
		s.pending = call{request["method"].(string), request["path"].(string), request["body"]}
		value := map[string]any{"id": "fixture-approval", "code": "TEST-CODE", "status": "approved"}
		raw, _ := json.Marshal(value)
		return true, call{}, json.Unmarshal(raw, result)
	}
	// Select the fixture behavior required to exercise this boundary.
	if path == "/v1/approvals/fixture-approval/execute" {
		return false, s.pending, nil
	}
	return false, call{method, path, body}, nil
}

// The PTY test's independent signer watches proposal receipts at a test-only
// gRPC proxy. Both hops use the real protocol; all authorization, enrollment
// and streaming run against the unmodified controller.
func withFixtureGRPCHuman(t *testing.T, c *controller.Controller, observers ...func(*pb.CommandRequest)) http.Handler {
	t.Helper()
	handler := c.Handler()
	signer := withFixtureHuman(t, c, handler).(*fixtureHuman)
	upstream := httptest.NewUnstartedServer(handler)
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	api := controlrpcconnect.NewControllerClient(upstream.Client(), upstream.URL, connect.WithGRPC())
	// Forward real commands and sign only the exact pending approval returned by the controller.
	proxy := connect.NewUnaryHandler(controlrpcconnect.ControllerCommandProcedure, func(ctx context.Context, in *connect.Request[pb.CommandRequest]) (*connect.Response[pb.CommandReply], error) {
		// Live integration tests observe coordinates and byte counts without replacing controller behavior.
		for _, observe := range observers {
			observe(in.Msg)
		}
		call := connect.NewRequest(in.Msg)
		call.Header().Set("Authorization", in.Header().Get("Authorization"))
		result, err := api.Command(ctx, call)
		// All nonapproval traffic and controller failures retain the original production response.
		if err == nil && in.Msg.Route == "/v1/approvals" && in.Msg.Method == "POST" {
			var approval map[string]any
			// Only a controller-retained pending request can receive a test authenticator decision.
			if json.Unmarshal(result.Msg.JsonBody, &approval) == nil && approval["status"] == "pending" {
				signer.mu.Lock()
				signed := signer.decide(approval, in.Header())
				signer.mu.Unlock()
				result.Msg.JsonBody = signed.Body.Bytes()
			}
		}
		return result, err
	})
	// Streaming and every other procedure continue directly to the controller's unmodified handler.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Observe only the unary command procedure; stream transport remains untouched.
		if r.URL.Path == controlrpcconnect.ControllerCommandProcedure {
			proxy.ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
}
