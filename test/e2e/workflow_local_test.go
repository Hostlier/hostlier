package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hostlier/internal/cli"
	"hostlier/internal/controller"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/internal/resources"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// localWorkflowAPI carries real CLI requests to the authenticated local controller handler.
type localWorkflowAPI struct{ handler http.Handler }

// Request exchanges actual controller JSON while avoiding unrelated public ingress setup.
func (api localWorkflowAPI) Request(ctx context.Context, method, path string, input, output any) error {
	raw, _ := json.Marshal(input)
	request := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	api.handler.ServeHTTP(recorder, request)
	// Preserve controller failures instead of simulating accepted workflow plans.
	if recorder.Code != http.StatusOK {
		var envelope struct {
			Error *model.Error `json:"error"`
		}
		json.Unmarshal(recorder.Body.Bytes(), &envelope)
		return envelope.Error
	}
	// Bodyless CLI calls still perform the actual controller operation.
	if output == nil {
		return nil
	}
	return json.Unmarshal(recorder.Body.Bytes(), output)
}

// localProbeRuntime launches real authenticated application probes on the returned dependency networks.
type localProbeRuntime struct {
	socket, owner string
	probes        atomic.Int32
	failure       atomic.Value
}

// Name preserves the production Docker resource ownership namespace.
func (*localProbeRuntime) Name() string { return "docker" }

// InspectImage supplies the pinned image used by this application probe fixture.
func (*localProbeRuntime) InspectImage(context.Context, string) (model.ImageInfo, error) {
	return model.ImageInfo{Digest: resources.Images["postgres"]}, nil
}

// Deploy starts isolated application containers after both real workflow packages complete.
func (r *localProbeRuntime) Deploy(ctx context.Context, p model.Project, c model.DeployContext) (model.Deployment, error) {
	// Package-generated credentials must be explicitly bound and accompanied by owned networks.
	r.failure.Store(fmt.Sprintf("Deploy received networks=%d bindings=%d", len(c.DependencyNetworks), len(c.Secrets)))
	// Both selected package bindings and networks must reach the application boundary.
	if len(c.DependencyNetworks) != 2 || len(c.Secrets) != 2 {
		return model.Deployment{}, fmt.Errorf("missing private workflow bindings")
	}
	// Both PostgreSQL and Redis must accept authenticated writes and reads from application containers.
	for _, engine := range []string{"postgres", "redis"} {
		// Probe containers use only the private network supplied by the deployment context.
		if err := r.probe(ctx, engine, c); err != nil {
			r.failure.Store(err.Error())
			return model.Deployment{}, err
		}
		r.probes.Add(1)
	}
	return model.Deployment{Name: p.Name, RuntimeID: "authenticated-application-probes", Status: "running"}, nil
}

// Action supplies the unused ordinary application lifecycle hook.
func (*localProbeRuntime) Action(context.Context, string, string, model.ActionContext) error {
	return nil
}

// Logs supplies no synthetic application output to the fixture.
func (*localProbeRuntime) Logs(context.Context, string) (string, error) { return "", nil }

// Doctor supplies the unused fixture runtime health hook.
func (*localProbeRuntime) Doctor(context.Context) (any, error) { return nil, nil }

// probe launches a disposable application client with passwords passed through environment inheritance.
func (r *localProbeRuntime) probe(ctx context.Context, engine string, c model.DeployContext) error {
	key := map[string]string{"postgres": "DATABASE_URL", "redis": "REDIS_URL"}[engine]
	address, err := url.Parse(c.Secrets[key])
	// Credentials and the target hostname are required before any application container starts.
	if err != nil || address.User == nil || !containsNetwork(c.DependencyNetworks, address.Hostname()) {
		return fmt.Errorf("workflow network binding mismatch")
	}
	args, environment := probeArguments(engine, address)
	args = append([]string{"--host", "unix://" + r.socket, "run", "--rm", "--network", address.Hostname(), "--label", "io.hostlier.owner=" + r.owner}, args...)
	command := exec.CommandContext(ctx, "docker", args...)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	// Never expose authenticated connection strings or process diagnostics in a failed assertion.
	if err != nil || !strings.Contains(string(output), "workflow-attached") {
		password, _ := address.User.Password()
		return fmt.Errorf("authenticated %s application probe failed: %s", engine, strings.ReplaceAll(string(output), password, "[REDACTED]"))
	}
	return nil
}

// containsNetwork verifies the package's target network was explicitly authorized by the controller.
func containsNetwork(networks []string, wanted string) bool {
	// Application probes cannot guess or attach to arbitrary runtime networks.
	for _, network := range networks {
		// The exact returned network is required before launching an application client.
		if network == wanted {
			return true
		}
	}
	return false
}

// probeArguments supplies fixed native commands and transient environment values for the selected engine.
func probeArguments(engine string, u *url.URL) ([]string, []string) {
	password, _ := u.User.Password()
	// PostgreSQL probes create application data and immediately read it through its private endpoint.
	if engine == "postgres" {
		return []string{"--env", "PGHOST", "--env", "PGPORT", "--env", "PGUSER", "--env", "PGDATABASE", "--env", "PGPASSWORD", resources.Images[engine], "psql", "-v", "ON_ERROR_STOP=1", "-Atc", "CREATE TABLE workflow_probe(value text); INSERT INTO workflow_probe VALUES ('workflow-attached'); SELECT value FROM workflow_probe"}, []string{"PGHOST=" + u.Hostname(), "PGPORT=" + u.Port(), "PGUSER=" + u.User.Username(), "PGDATABASE=" + strings.TrimPrefix(u.Path, "/"), "PGPASSWORD=" + password}
	}
	return []string{"--env", "REDISCLI_AUTH", resources.Images[engine], "sh", "-c", "redis-cli --user \"$2\" -h \"$1\" SET workflow-probe workflow-attached >/dev/null && redis-cli --user \"$2\" -h \"$1\" GET workflow-probe", "probe", u.Hostname(), u.User.Username()}, []string{"REDISCLI_AUTH=" + password}
}

// TestDockerWorkflowNewInstancesApplicationAttachment covers approved local package provisioning and real app connectivity.
func TestDockerWorkflowNewInstancesApplicationAttachment(t *testing.T) {
	testLocalWorkflowAttachment(t, false)
}

// TestDockerCustomProductApplicationAttachment expands a local product before approval and actual engine binding.
func TestDockerCustomProductApplicationAttachment(t *testing.T) {
	testLocalWorkflowAttachment(t, true)
}

// testLocalWorkflowAttachment shares owned infrastructure and real probes across declaration formats.
func testLocalWorkflowAttachment(t *testing.T, custom bool) {
	t.Helper()
	// Require explicit opt-in before creating disposable Docker infrastructure.
	if os.Getenv("HOSTLIER_DOCKER_E2E") != "1" {
		t.Skip("set HOSTLIER_DOCKER_E2E=1 for real installed-package application attachment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	// Canonical private storage satisfies the protected package installation contract.
	if err != nil {
		t.Fatal(err)
	}
	socket := os.Getenv("HOSTLIER_DOCKER_SOCKET")
	// Keep the existing Docker integration fixture's socket default.
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	hash := sha256.Sum256([]byte(directory))
	owner := hex.EncodeToString(hash[:])[:16]
	cleanupDockerOwner(t, socket, owner)
	installWorkflowEngines(t, ctx, directory)
	runtime := &localProbeRuntime{socket: socket, owner: owner}
	runLocalWorkflowDeployment(t, ctx, directory, runtime, custom)
}

// installWorkflowEngines builds and explicitly installs the actual PostgreSQL and Redis package executables.
func installWorkflowEngines(t *testing.T, ctx context.Context, directory string) {
	t.Helper()
	runner := packageexec.Runner{Directory: filepath.Join(directory, "packages")}
	// The real package runner will execute each binary through its protected snapshot facility.
	for _, engine := range []string{"postgres", "redis"} {
		source := filepath.Join(directory, "hostlier-"+engine)
		build := exec.CommandContext(ctx, "go", "build", "-o", source, "./cmd/hostlier-"+engine)
		build.Dir = filepath.Join("..", "..")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		// Build failures must include compiler diagnostics without starting controller work.
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", engine, err, output)
		}
		// The generic installer measures and activates administrator-selected executable bytes.
		if _, err := runner.Install(engine, source); err != nil {
			t.Fatal(err)
		}
	}
}

// runLocalWorkflowDeployment uses strict .hostlier parsing, one approval, and the production package runner.
func runLocalWorkflowDeployment(t *testing.T, ctx context.Context, directory string, runtime *localProbeRuntime, custom bool) {
	t.Helper()
	server, err := controller.New(controller.Options{StateDir: directory, Config: model.ServerConfig{Version: 1, Domains: []string{"hostlier.test"}}, Runtime: runtime})
	// Production initialization must select the real executable package runner.
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	project := "version: 1\nname: workflow-app\nimage: " + resources.Images["postgres"] + "\nport: 80\nroute: workflow-app.hostlier.test\nworkflow:\n  steps:\n    - id: database\n      package: postgres\n      config:\n        mode: new-instance\n        storage: 1Gi\n    - id: cache\n      package: redis\n      config:\n        mode: new-instance\n        storage: 1Gi\n  bindings:\n    DATABASE_URL: database.url\n    REDIS_URL: cache.url\n"
	// Local product definitions expand to the same installed package graph.
	if custom {
		project = customProductDocument()
	}
	// The CLI reads the actual strict project document rather than a manually constructed spec.
	if err = os.WriteFile(filepath.Join(directory, ".hostlier"), []byte(project), 0600); err != nil {
		t.Fatal(err)
	}
	runApprovedWorkflowCLI(t, ctx, directory, runtime, server.LocalHandler(), custom)
}

// runApprovedWorkflowCLI checks approval coverage and secret-free public output after actual probes succeed.
func runApprovedWorkflowCLI(t *testing.T, ctx context.Context, directory string, runtime *localProbeRuntime, handler http.Handler, custom bool) {
	t.Helper()
	prompts := 0
	var output bytes.Buffer
	// One explicit approval covers both package plans and subsequent application deployment.
	approve := func(string, bool) (string, error) { prompts++; return "yes", nil }
	args := []string{"deploy"}
	// Product deploy uses the same plan review and protected apply path.
	if custom {
		args = []string{"product", "deploy"}
	}
	err := cli.RunWithOptions(ctx, args, strings.NewReader(""), &output, io.Discard, cli.Options{CWD: directory, Home: filepath.Join(directory, "home"), Client: localWorkflowAPI{handler: handler}, Prompt: approve, PollInterval: 10 * time.Millisecond})
	// The operation succeeds only after both real application containers authenticate and read back data.
	if err != nil || prompts != 1 || runtime.probes.Load() != 2 {
		t.Fatalf("workflow deployment: %v approvals=%d probes=%d diagnostic=%v output=%s", err, prompts, runtime.probes.Load(), runtime.failure.Load(), output.String())
	}
	// Public CLI output must not reveal either generated authenticated connection string.
	if strings.Contains(output.String(), "postgresql://hostlier:") || strings.Contains(output.String(), "redis://hostlier:") {
		t.Fatal("workflow connection leaked")
	}
}

// customProductDocument declares native dependencies and typed inputs with no author executable source.
func customProductDocument() string {
	return `version: 1
product:
  version: 1
  name: local-data-app
  revision: "1.0.0"
  inputs:
    storage:
      type: string
      required: true
      enum: [1Gi, 2Gi]
  steps:
    - id: database
      package: postgres
      config:
        mode: new-instance
      configInputs:
        storage: storage
    - id: cache
      package: redis
      config:
        mode: new-instance
      configInputs:
        storage: storage
  bindings:
    DATABASE_URL: database.url
    REDIS_URL: cache.url
inputs:
  storage: 1Gi
app:
  version: 1
  name: workflow-app
  image: ` + resources.Images["postgres"] + `
  port: 80
  route: workflow-app.hostlier.test
`
}
