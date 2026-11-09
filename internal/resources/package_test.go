package resources

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/controller"
	"hostlier/internal/model"
	"hostlier/internal/resourceclient"
)

// resourceTestRuntime supplies inert application hooks for resource controller integration.
type resourceTestRuntime struct{}

// Name identifies the resource backend.
func (resourceTestRuntime) Name() string { return "docker" }

// InspectImage keeps application image inspection outside this resource fixture.
func (resourceTestRuntime) InspectImage(context.Context, string) (model.ImageInfo, error) {
	return model.ImageInfo{}, nil
}

// Deploy rejects application deployments in this resource-only runtime fixture.
func (resourceTestRuntime) Deploy(context.Context, model.Project, model.DeployContext) (model.Deployment, error) {
	return model.Deployment{}, fmt.Errorf("applications are outside this resource test")
}

// Action supplies an unused no-op application hook for the resource-only fixture.
func (resourceTestRuntime) Action(context.Context, string, string, model.ActionContext) error {
	return nil
}

// Logs supplies an empty application diagnostic hook for the resource-only fixture.
func (resourceTestRuntime) Logs(context.Context, string) (string, error) { return "", nil }

// Doctor supplies the unused runtime health hook for the resource fixture.
func (resourceTestRuntime) Doctor(context.Context) (any, error) { return nil, nil }

const resourcePTY = `
import os,pty,subprocess,sys,select,time
master,slave=pty.openpty()
p=subprocess.Popen(sys.argv[1:],stdin=slave,stdout=slave,stderr=slave,env=os.environ)
os.close(slave)
output=b''
sent=False
end=time.time()+180
while time.time()<end:
  if select.select([master],[],[],0.1)[0]:
    try: chunk=os.read(master,65536)
    except OSError: break
    if not chunk: break
    output+=chunk
    if b'Approve this exact' in output and not sent:
      os.write(master,b'yes\n');sent=True
  if p.poll() is not None: break
if p.poll() is None: p.kill()
sys.stdout.buffer.write(output)
sys.exit(p.wait())
`

// TestLivePackagedCLIResourceBackupAndRestore checks approved native backup and restore through the packaged CLI and HTTPS.
func TestLivePackagedCLIResourceBackupAndRestore(t *testing.T) {
	// Require explicit opt-in before running disposable live infrastructure tests.
	if os.Getenv("HOSTLIER_LIVE_RESOURCE_CLI") != "1" {
		t.Skip("set HOSTLIER_LIVE_RESOURCE_CLI=1 for real Docker resource tests through the executable and HTTPS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("/tmp", "hl-resource-cli-")
	// Require an isolated fixture directory before creating controller state.
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir = canonicalResourceFixture(t, dir)
	binary := buildPackagedResourceCLI(t, ctx, dir)
	installPackagedResources(t, ctx, dir, binary)
	socketPath := os.Getenv("HOSTLIER_DOCKER_SOCKET")
	generic, err := New(Options{StateDir: filepath.Join(dir, "state"), SocketPath: socketPath})
	// Require the selected resource backend to initialize successfully.
	if err != nil {
		t.Fatal(err)
	}
	manager := generic.(*Manager)
	broker, _ := resourceclient.New(resourceclient.Options{Type: "docker", StateDir: filepath.Join(dir, "state"), SocketPath: socketPath})
	checkPackagedDigestApproval(t, ctx, dir, broker, manager)
	server, err := controller.New(controller.Options{StateDir: filepath.Join(dir, "state"), Config: model.ServerConfig{Version: 1, Domains: []string{"example.test"}}, Runtime: resourceTestRuntime{}, ResourceManager: broker})
	// Require the selected resource backend to initialize successfully.
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	socket := filepath.Join(dir, "controller.sock")
	// Require the explicitly selected local fixture listener to start.
	if err = server.Listen(controller.ListenOptions{SocketPath: socket}); err != nil {
		t.Fatal(err)
	}
	https := httptest.NewTLSServer(server.Handler())
	defer https.Close()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: https.Certificate().Raw}), 0600)
	env := append(os.Environ(), "HOSTLIER_HOME="+filepath.Join(dir, "profiles"), "HOSTLIER_CA_FILE="+ca)
	// Run the packaged CLI with controlled stdin and the isolated profile and CA.
	run := func(args []string, input string) ([]byte, error) {
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = env
		command.Stdin = strings.NewReader(input)
		return command.CombinedOutput()
	}
	local, _ := client.New(client.Options{SocketPath: socket})
	var issued struct {
		Token string `json:"token"`
	}
	// Require the authenticated controller request to complete successfully.
	if err = local.Request(ctx, "POST", "/v1/pairing/tokens", map[string]string{"name": "resource-test"}, &issued); err != nil {
		t.Fatal(err)
	}
	// Fail the fixture on connect.
	if output, err := run([]string{"connect", https.URL, "--name", "test", "--token-stdin"}, issued.Token); err != nil {
		t.Fatalf("connect: %v %s", err, output)
	}
	// Run approval prompts through a pseudo-terminal using the isolated CLI environment.
	pty := func(args ...string) ([]byte, error) {
		parameters := append([]string{"-c", resourcePTY, binary}, args...)
		command := exec.CommandContext(ctx, "python3", parameters...)
		command.Env = env
		return command.CombinedOutput()
	}
	credentials := packagedResourceCredentials(t, ctx, manager)
	// Purge the source and restored Docker fixtures when the packaged CLI test exits.
	defer func() {
		manager.Action(context.Background(), "app-db", "delete", model.ResourceContext{Purge: true})
		manager.Action(context.Background(), "restored-db", "delete", model.ResourceContext{Purge: true})
	}()
	output, err := pty("resource", "create", "postgres", "app-db", "--storage", "1Gi")
	// Fail the fixture on resource create.
	if err != nil {
		t.Fatalf("resource create: %v %s", err, output)
	}
	// Fail the fixture on creation did not complete.
	if !strings.Contains(string(output), "succeeded") {
		t.Fatalf("creation did not complete: %s", output)
	}
	checkPackagedBackupRestore(t, ctx, manager, dir, output, run, pty, credentials)
}

// packagedResourceCredentials reads the independently generated PostgreSQL credential from Docker.
func packagedResourceCredentials(t *testing.T, ctx context.Context, manager *Manager) func(string) model.ResourceCredentials {
	// Read the selected fixture container password for later authenticated protocol assertions.
	return func(name string) model.ResourceCredentials {
		var current struct{ Config struct{ Env []string } }
		// Require the owned Docker API request to complete before using its result.
		if _, err := manager.api(ctx, "GET", "/containers/"+manager.container(name)+"/json", nil, &current); err != nil {
			t.Fatal(err)
		}
		value := model.ResourceCredentials{Username: "hostlier", Database: "app"}
		// Read only the expected database password from the container environment.
		for _, entry := range current.Config.Env {
			// Extract only the expected PostgreSQL password environment entry.
			if strings.HasPrefix(entry, "POSTGRES_PASSWORD=") {
				value.Password = strings.TrimPrefix(entry, "POSTGRES_PASSWORD=")
			}
		}
		// Fail the fixture on resource did not receive a password.
		if value.Password == "" {
			t.Fatal("resource did not receive a password")
		}
		return value
	}
}

// checkPackagedBackupRestore exercises protected export and approved independent restore.
func checkPackagedBackupRestore(t *testing.T, ctx context.Context, manager *Manager, dir string, output []byte, run func([]string, string) ([]byte, error), pty func(...string) ([]byte, error), credentials func(string) model.ResourceCredentials) {
	t.Helper()
	var err error
	source := credentials("app-db")
	// Fail the fixture on creation plan exposed password.
	if strings.Contains(string(output), source.Password) {
		t.Fatal("creation plan exposed password")
	}
	query := []string{"psql", "-h", "127.0.0.1", "-U", "hostlier", "-d", "app", "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE fixture_data(value text); INSERT INTO fixture_data VALUES ('packaged-value')"}
	// Return native command failures instead of accepting incomplete engine output.
	if _, err = manager.execute(ctx, "app-db", query, authEnv("postgres", source)); err != nil {
		t.Fatal(err)
	}
	checkPackagedResize(t, ctx, manager, pty, source)
	output, err = run([]string{"resource", "backup", "app-db", "--output", filepath.Join(dir, "export.dump")}, "")
	// Fail the fixture on backup/export.
	if err != nil {
		t.Fatalf("backup/export: %v %s", err, output)
	}
	var exported struct {
		BackupID string `json:"backupId"`
	}
	// Fail the fixture on backup metadata.
	if err = json.Unmarshal(output, &exported); err != nil || exported.BackupID == "" {
		t.Fatalf("backup metadata: %v %s", err, output)
	}
	file, err := os.Stat(filepath.Join(dir, "export.dump"))
	// Fail the fixture on exported backup not protected/nonempty.
	if err != nil || file.Mode().Perm() != 0600 || file.Size() == 0 {
		t.Fatal("exported backup not protected/nonempty")
	}
	checkPackagedRestoredData(t, ctx, manager, exported.BackupID, source, pty, credentials)
}

// buildPackagedResourceCLI builds the executable exercised by the resource integration test.
func buildPackagedResourceCLI(t *testing.T, ctx context.Context, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, "hostlier")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/hostlier")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	// Fail with compiler diagnostics if the packaged CLI cannot be built.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %v %s", err, output)
	}
	return binary
}

// checkPackagedRestoredData verifies approved restore uses a new password and restores SQL data.
func checkPackagedRestoredData(t *testing.T, ctx context.Context, manager *Manager, backupID string, source model.ResourceCredentials, pty func(...string) ([]byte, error), credentials func(string) model.ResourceCredentials) {
	t.Helper()
	output, err := pty("resource", "restore", "postgres", "restored-db", "--storage", "1Gi", "--backup", backupID)
	// Fail the fixture on approved restore.
	if err != nil {
		t.Fatalf("approved restore: %v %s", err, output)
	}
	target := credentials("restored-db")
	// Fail the fixture on restored instance reused source credential.
	if target.Password == source.Password {
		t.Fatal("restored instance reused source credential")
	}
	read := []string{"psql", "-h", "127.0.0.1", "-U", "hostlier", "-d", "app", "-Atc", "SELECT value FROM fixture_data"}
	data, err := manager.execute(ctx, "restored-db", read, authEnv("postgres", target))
	// Fail the fixture on restored protocol query.
	if err != nil || !strings.Contains(string(data), "packaged-value") {
		t.Fatalf("restored protocol query: %v %s", err, data)
	}
}

// checkPackagedResize exercises HTTPS exact approval and independently installed resize RPC.
func checkPackagedResize(t *testing.T, ctx context.Context, m *Manager, pty func(...string) ([]byte, error), c model.ResourceCredentials) {
	t.Helper()
	output, err := pty("resource", "scale", "app-db", "--cpu", "0.5", "--memory", "256Mi")
	// The reviewed maintenance operation must finish through the packaged CLI.
	if err != nil || !strings.Contains(string(output), "succeeded") {
		t.Fatalf("packaged resize: %v %s", err, output)
	}
	query := []string{"psql", "-h", "127.0.0.1", "-U", "hostlier", "-d", "app", "-Atc", "SELECT value FROM fixture_data"}
	data, err := m.execute(ctx, "app-db", query, authEnv("postgres", c))
	// Existing contents and credentials must survive allocation maintenance.
	if err != nil || !strings.Contains(string(data), "packaged-value") {
		t.Fatalf("packaged resize data: %v %s", err, data)
	}
	current, err := m.inspect(ctx, "app-db")
	// Verify runtime enforcement rather than relying on the approval response alone.
	if err != nil || current.HostConfig["NanoCpus"] != float64(500000000) || current.HostConfig["Memory"] != float64(268435456) {
		t.Fatal("packaged resize limits", err, current.HostConfig)
	}
}
