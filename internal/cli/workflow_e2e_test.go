package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/artifact"
	"hostlier/internal/client"
	"hostlier/internal/controller"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// workflowAPI exercises the real controller handler without requiring a TCP or Unix listener.
type workflowAPI struct {
	handler http.Handler
	*client.Client
}

// Run watch subscriptions over a real local HTTP/2 connection. Keep the
// command fixture separate so individual tests can still inject approvals.
func newWorkflowAPI(t *testing.T, handler http.Handler) workflowAPI {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	rpc, err := client.NewGRPC(client.Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	rpc.HTTP = srv.Client()
	return workflowAPI{handler: handler, Client: rpc}
}

// Request preserves the CLI request/response contract over an in-process HTTP exchange.
func (api workflowAPI) Request(ctx context.Context, method, path string, input, output any) error {
	raw, _ := json.Marshal(input)
	request := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	api.handler.ServeHTTP(recorder, request)
	// Decode real controller failures instead of simulating successful CLI responses.
	if recorder.Code != 200 {
		var envelope struct {
			Error *model.Error `json:"error"`
		}
		json.Unmarshal(recorder.Body.Bytes(), &envelope)
		return envelope.Error
	}
	// Bodyless CLI calls still complete the actual controller mutation.
	if output == nil {
		return nil
	}
	return json.Unmarshal(recorder.Body.Bytes(), output)
}

// workflowEngine records package completion before accepting the application deployment.
type workflowEngine struct {
	packageCalls int
	deployed     bool
}

// Name selects the existing Docker-compatible controller state namespace.
func (*workflowEngine) Name() string { return "docker" }

// InspectImage returns a stable immutable image for review.
func (*workflowEngine) InspectImage(_ context.Context, image string) (model.ImageInfo, error) {
	// Registry preparation already verified this controller-owned content; retain its exact identity.
	if artifact.IsImage(image) {
		return model.ImageInfo{Digest: image}, nil
	}
	return model.ImageInfo{Digest: "example/app@sha256:" + strings.Repeat("a", 64)}, nil
}

// Deploy asserts provisioning and private output binding happened before application startup.
func (e *workflowEngine) Deploy(_ context.Context, _ model.Project, input model.DeployContext) (model.Deployment, error) {
	// Only the selected connection may enter the application environment.
	if e.packageCalls != 1 || input.Secrets["DATABASE_URL"] != "private-app-url" || input.Secrets["executionToken"] != "" {
		return model.Deployment{}, model.Err("BAD_ORDER", "Invalid workflow order or bindings")
	}
	e.deployed = true
	return model.Deployment{RuntimeID: "fixture", Status: "running"}, nil
}

// Action satisfies the ordinary app lifecycle contract used by the controller.
func (*workflowEngine) Action(context.Context, string, string, model.ActionContext) error { return nil }

// Logs supplies a harmless diagnostic fixture.
func (*workflowEngine) Logs(context.Context, string) (string, error) { return "", nil }

// Doctor supplies a harmless readiness fixture.
func (*workflowEngine) Doctor(context.Context) (any, error) { return map[string]bool{"ok": true}, nil }

// Plan returns an immutable package fixture without modifying provisioning state.
func (*workflowEngine) Plan(context.Context, workflow.Request) (workflow.Prepared, error) {
	return workflow.Prepared{Digest: strings.Repeat("a", 64), Summary: []string{"Provision PostgreSQL before deployment"}, Payload: json.RawMessage(`{"reviewed":true}`)}, nil
}

// Apply consumes the protected credential only after the deployment approval.
func (e *workflowEngine) Apply(_ context.Context, req workflow.Request, _ string) (workflow.Result, error) {
	// The CLI must not replace or lose a pinned private provider credential.
	if req.Secrets["executionToken"] != "private-fused-token" {
		return workflow.Result{}, model.Err("BAD_SECRET", "Missing package credential")
	}
	e.packageCalls++
	return workflow.Result{State: json.RawMessage(`{"owned":true}`), Outputs: map[string]string{"url": "private-app-url"}}, nil
}

// TestWorkflowCLIEndToEnd covers .hostlier parsing, review, approval, controller execution and app binding together.
func TestWorkflowCLIEndToEnd(t *testing.T) {
	dir := t.TempDir()
	engine := &workflowEngine{}
	c, err := controller.New(controller.Options{StateDir: filepath.Join(dir, "state"), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: engine, Packages: engine})
	// Real durable controller construction must succeed before exercising the CLI.
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	api := newWorkflowAPI(t, withFixtureHuman(t, c, c.LocalHandler()))
	// Upload through the ordinary secret API before constructing the deployment plan.
	if err := api.Request(context.Background(), "PUT", "/v1/secrets/fused-token", map[string]any{"value": "private-fused-token", "requestId": "upload"}, nil); err != nil {
		t.Fatal(err)
	}
	projectFile := "version: 1\nname: sample\nimage: example/app:latest\nport: 80\nroute: sample.example.com\nworkflow:\n  steps:\n    - id: database\n      package: aiven\n      config:\n        engine: postgres\n        mode: new-instance\n      secrets:\n        executionToken: fused-token\n  bindings:\n    DATABASE_URL: database.url\n"
	// Use the actual strict YAML reader through the CLI's project discovery path.
	if err := os.WriteFile(filepath.Join(dir, ".hostlier"), []byte(projectFile), 0600); err != nil {
		t.Fatal(err)
	}
	prompts := 0
	// Exactly one deployment approval covers the reviewed package work and application change.
	prompt := func(string, bool) (string, error) { prompts++; return "yes", nil }
	var output bytes.Buffer
	err = RunWithOptions(context.Background(), []string{"deploy"}, strings.NewReader(""), &output, io.Discard, Options{CWD: dir, Home: filepath.Join(dir, "home"), Client: api, Prompt: prompt, PollInterval: time.Millisecond})
	// CLI completion must reflect the actual successful asynchronous controller operation.
	if err != nil || prompts != 1 || !engine.deployed {
		t.Fatal(err, prompts, engine.deployed)
	}
	text := output.String()
	// Review contains package intent while keeping all secret values private.
	if !strings.Contains(text, "Provision PostgreSQL") || strings.Contains(text, "private-fused-token") || strings.Contains(text, "private-app-url") {
		t.Fatal("missing review or exposed credential")
	}
}
