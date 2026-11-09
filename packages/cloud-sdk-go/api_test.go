package cloudsdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectAPIProvisionAndPurgeWithSignedCredential(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	epoch := strings.Repeat("a", 64)
	var planned map[string]any
	var approvedPath string
	var exists bool
	var calls []string
	server := newSDKServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/email/session" {
			_ = json.NewEncoder(w).Encode(map[string]string{"epoch": epoch})
			return
		}
		body, _ := io.ReadAll(r.Body)
		hash := sha256.Sum256(body)
		message := strings.Join([]string{"hostlier-device-v1", epoch, r.Method, r.URL.RequestURI(), r.Header.Get("X-Hostlier-Time"), r.Header.Get("X-Hostlier-Nonce"), hex.EncodeToString(hash[:])}, "\x00")
		signature, _ := base64.RawStdEncoding.DecodeString(r.Header.Get("X-Hostlier-Signature"))
		if r.Header.Get("Authorization") != "Bearer test-token" || !ed25519.Verify(public, []byte(message), signature) {
			t.Error("unsigned or incorrectly signed controller request", r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/plans":
			var request struct {
				Spec map[string]any `json:"spec"`
			}
			_ = json.Unmarshal(body, &request)
			planned = request.Spec
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "plan-1", "fingerprint": "fingerprint-1"})
		case "POST /v1/approvals":
			var request struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(body, &request)
			approvedPath = request.Path
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "approval-1", "status": "approved"})
		case "POST /v1/approvals/approval-1/execute":
			if strings.Contains(approvedPath, "/plans/") {
				exists = true
				_ = json.NewEncoder(w).Encode(map[string]string{"operationId": "operation-create"})
			} else if strings.HasSuffix(approvedPath, "/action") {
				exists = false
				_ = json.NewEncoder(w).Encode(map[string]string{"operationId": "operation-purge"})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]bool{"stored": true})
			}
		case "GET /v1/operations/operation-create/events", "GET /v1/operations/operation-purge/events":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": strings.Split(r.URL.Path, "/")[3], "status": "succeeded"})
		case "GET /v1/apps/acme-app":
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "Application not found"}})
				return
			}
			spec := map[string]any{}
			for key, value := range planned {
				spec[key] = value
			}
			image := "ghcr.io/acme/tool@sha256:" + strings.Repeat("b", 64)
			spec["image"] = image
			if source, ok := spec["source"].(map[string]any); ok {
				source["image"] = image
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "acme-app", "status": "running", "url": "https://acme.apps.example.com", "spec": spec})
		case "GET /v1/status":
			apps := []any{}
			if exists {
				apps = append(apps, map[string]string{"name": "acme-app"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"applications": apps, "operations": []any{}})
		default:
			t.Errorf("unexpected controller API request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	bundle := filepath.Join(root, "vendor.hostlier")
	if err := os.WriteFile(bundle, []byte(sampleBundle), 0600); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(root, "controller.json")
	device, _ := json.Marshal(map[string]string{"token": "test-token", "privateKey": base64.RawStdEncoding.EncodeToString(private)})
	file, _ := json.Marshal(map[string]string{"controllerUrl": server.URL, "credential": "device-v1:" + base64.RawStdEncoding.EncodeToString(device)})
	if err := os.WriteFile(credential, file, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := Open(Options{BundlePath: bundle, StateDirectory: filepath.Join(root, "state"), CredentialFile: credential})
	if err != nil {
		t.Fatal(err)
	}
	client.runner.(*apiRunner).http = server.Client()
	if err := client.SetInstanceSecret(context.Background(), "acme-app", "acme-key", []byte("private")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Create(context.Background(), CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Purge(context.Background(), "acme-app"); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("purge left the app active")
	}
	if plannedSecrets, ok := planned["secrets"].(map[string]any); !ok || plannedSecrets["API_KEY"] != "vendor-key" {
		t.Fatal("controller plan did not lower stored secret reference", planned["secrets"])
	}
	for _, expected := range []string{"POST /v1/plans", "POST /v1/approvals", "POST /v1/approvals/approval-1/execute", "GET /v1/operations/operation-create/events", "GET /v1/status"} {
		if !containsAPI(calls, expected) {
			t.Fatal("missing direct controller API call", expected, calls)
		}
	}
}

func TestDirectAPILowersSidecarStoredSecrets(t *testing.T) {
	project := map[string]any{
		"secrets":  map[string]any{"TOKEN": "secret://customer-token"},
		"sidecars": map[string]any{"storage": map[string]any{"secrets": map[string]any{"ROOT_KEY": "secret://customer-key"}}},
	}
	if err := lowerStoredReferences(project, false); err != nil {
		t.Fatal(err)
	}
	if project["secrets"].(map[string]any)["TOKEN"] != "customer-token" || project["sidecars"].(map[string]any)["storage"].(map[string]any)["secrets"].(map[string]any)["ROOT_KEY"] != "customer-key" {
		t.Fatal("nested stored secret references were not lowered", project)
	}
	if err := lowerStoredReferences(map[string]any{"secrets": map[string]any{"TOKEN": "plaintext"}}, false); err == nil {
		t.Fatal("accepted an implicit plaintext secret")
	}
}

func TestRejectedPlanClearsPendingIntent(t *testing.T) {
	client, runner, root := testClient(t)
	runner.fail = "deploy"
	// A definite controller plan rejection is safe to fix and retry; no apply
	// request or operation was accepted.
	client.runner = &rejectedPlanRunner{runner}
	if _, err := client.Create(context.Background(), CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err == nil {
		t.Fatal("accepted rejected plan")
	}
	if _, err := os.Stat(filepath.Join(root, "state", "acme-app", "pending.json")); !os.IsNotExist(err) {
		t.Fatal("definitive plan rejection retained pending intent", err)
	}
}

type rejectedPlanRunner struct{ *fakeRunner }

func (r *rejectedPlanRunner) Run(ctx context.Context, args []string, directory string) (Result, error) {
	if len(args) > 0 && args[0] == "deploy" {
		return Result{}, &planRejectedError{&apiFailure{Code: "INVALID_CONFIG", Message: "Invalid secret binding"}}
	}
	return r.fakeRunner.Run(ctx, args, directory)
}

func TestDirectAPIRejectsBearerOnlyAndPublicCredentialFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "credential.json")
	if err := os.WriteFile(path, []byte(`{"controllerUrl":"https://controller.example.com","credential":"bearer-only"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newAPIRunner(path); err == nil {
		t.Fatal("accepted bearer-only credential")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newAPIRunner(path); err == nil {
		t.Fatal("accepted publicly readable credential")
	}
}

func TestDirectAPIWorkflowUsesDurablePlanning(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	epoch := strings.Repeat("a", 64)
	var calls []string
	server := newSDKServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/email/session" {
			_ = json.NewEncoder(w).Encode(map[string]string{"epoch": epoch})
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/planning":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "job-1", "status": "running"})
		case "GET /v1/planning/job-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "job-1", "status": "completed", "plan": map[string]string{"id": "plan-1", "fingerprint": "fingerprint-1"}})
		default:
			t.Errorf("unexpected planning route: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	a := &apiRunner{origin: server.URL, token: "token", key: private, http: server.Client()}
	plan, err := a.reviewedPlan(context.Background(), map[string]any{"spec": map[string]any{"workflow": map[string]any{"steps": []any{}}}}, true)
	if err != nil || plan.ID != "plan-1" || plan.Fingerprint != "fingerprint-1" {
		t.Fatal("workflow planning failed", plan, err)
	}
	if len(calls) != 2 || calls[0] != "POST /v1/planning" || calls[1] != "GET /v1/planning/job-1" {
		t.Fatal("workflow bypassed durable planning", calls)
	}
}

func containsAPI(calls []string, expected string) bool {
	for _, call := range calls {
		if call == expected {
			return true
		}
	}
	return false
}
