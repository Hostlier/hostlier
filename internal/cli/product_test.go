package cli

import (
	"bytes"
	"context"
	"hostlier/internal/controller"
	"hostlier/internal/model"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// productFixture is a local product containing an installed package reference and stored secret alias.
const productFixture = `version: 1
product:
  version: 1
  name: private-data-app
  revision: "1.0.0"
  inputs:
    storage:
      type: string
      required: true
      enum: [1Gi, 2Gi]
  secrets: [admin]
  steps:
    - id: database
      package: postgres
      config:
        mode: new-instance
      configInputs:
        storage: storage
      secretInputs:
        executionToken: admin
  bindings:
    DATABASE_URL: database.url
inputs:
  storage: 1Gi
secretRefs:
  admin: fused-token
app:
  version: 1
  name: sample
  image: example/app:latest
  port: 80
  route: sample.example.com
`

// TestProductCLIApprovalAndBinding covers strict composition, declined approval and approved app binding.
func TestProductCLIApprovalAndBinding(t *testing.T) {
	dir := t.TempDir()
	engine := &workflowEngine{}
	server, err := controller.New(controller.Options{StateDir: filepath.Join(dir, "state"), Config: model.ServerConfig{Version: 1, Domains: []string{"example.com"}}, Runtime: engine, Packages: engine})
	// Real controller construction establishes durable plans before CLI requests.
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	api := newWorkflowAPI(t, withFixtureHuman(t, server, server.LocalHandler()))
	// Store private values through the same API used by ordinary deployments.
	if err = api.Request(context.Background(), "PUT", "/v1/secrets/fused-token", map[string]any{"value": "private-fused-token", "requestId": "product-upload"}, nil); err != nil {
		t.Fatal(err)
	}
	// The CLI reads the strict envelope rather than a manually composed project.
	if err = os.WriteFile(filepath.Join(dir, ".hostlier"), []byte(productFixture), 0600); err != nil {
		t.Fatal(err)
	}
	// Supply the fixture's explicit interactive approval response.
	options := Options{CWD: dir, Home: filepath.Join(dir, "home"), Client: api, PollInterval: time.Millisecond, Prompt: func(string, bool) (string, error) { return "no", nil }}
	var output bytes.Buffer
	err = RunWithOptions(context.Background(), []string{"product", "deploy"}, strings.NewReader(""), &output, io.Discard, options)
	// Declining product approval may plan packages but must not apply or start the app.
	if !declinedProduct(err, engine) {
		t.Fatal("declined product mutated runtime", err)
	}
	// Approve only after the current public plan has been displayed.
	options.Prompt = func(string, bool) (string, error) { return "yes", nil }
	err = RunWithOptions(context.Background(), []string{"product", "deploy"}, strings.NewReader(""), &output, io.Discard, options)
	// Approved ordinary controller execution must resolve the selected private binding.
	if err != nil || !engine.deployed || engine.packageCalls != 1 {
		t.Fatal("product deployment failed", err)
	}
	// Product public output must preserve ordinary secret redaction guarantees.
	if strings.Contains(output.String(), "private-fused-token") || strings.Contains(output.String(), "private-app-url") {
		t.Fatal("product leaked private output")
	}
}

// TestProductStrictDecode rejects unknown executable fields before package invocation.
func TestProductStrictDecode(t *testing.T) {
	_, err := decodeProductProject([]byte(strings.Replace(productFixture, "      package: postgres", "      package: postgres\n      command: arbitrary-script", 1)))
	// Product authors cannot add source or execution commands to an installed package reference.
	if err == nil {
		t.Fatal("unknown executable field accepted")
	}
}

// declinedProduct verifies no accepted product effects escaped a declined review.
func declinedProduct(err error, engine *workflowEngine) bool {
	return err != nil && engine.packageCalls == 0 && !engine.deployed
}
