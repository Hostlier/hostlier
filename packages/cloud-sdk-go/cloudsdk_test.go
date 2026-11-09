package cloudsdk

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/config"
)

const sampleBundle = `version: 2
name: vendor-template
source:
  image: ghcr.io/acme/tool:1.0.0
workload: service
endpoint:
  exposure: public
  port: 3000
  route: template.apps.example.com
health:
  path: /health
resources:
  cpu: "1"
  memory: 1Gi
env:
  NODE_ENV: production
secrets:
  API_KEY: secret://vendor-key
volumes:
  data:
    mount: /app/data
    size: 5Gi
`

type fakeRunner struct {
	calls       [][]string
	deployed    map[string]any
	fail        string
	secretArgs  []string
	secretInput []byte
	statusReady bool
	operation   map[string]any
}

func (f *fakeRunner) RunInput(_ context.Context, args []string, _ string, input []byte) (Result, error) {
	f.secretArgs = append([]string(nil), args...)
	f.secretInput = append([]byte(nil), input...)
	if f.fail == "secret" {
		return Result{Stderr: string(input)}, errors.New("APPROVAL_LIMIT: Too many retained approval requests")
	}
	return Result{Stdout: `{}`}, nil
}

func (f *fakeRunner) Run(_ context.Context, args []string, _ string) (Result, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) == 0 {
		return Result{}, errors.New("missing command")
	}
	if args[0] == f.fail {
		return Result{}, errors.New("connection interrupted")
	}
	if args[0] == "status" {
		if args[1] == "--json" {
			apps := []map[string]any{}
			if f.deployed != nil {
				apps = append(apps, map[string]any{"name": f.deployed["name"]})
			}
			operations := []map[string]any{}
			if f.operation != nil {
				operations = append(operations, f.operation)
			}
			data, _ := json.Marshal(map[string]any{"applications": apps, "operations": operations})
			return Result{Stdout: string(data)}, nil
		}
		status := map[string]any{"name": f.deployed["name"], "spec": f.deployed}
		if f.statusReady {
			status["status"] = "running"
			status["url"] = "https://acme.apps.example.com"
			status["runtimeStatus"] = map[string]any{"running": true, "readyReplicas": 1}
		}
		data, _ := json.Marshal(status)
		return Result{Stdout: string(data)}, nil
	}
	if args[0] == "operation" {
		data, _ := json.Marshal(f.operation)
		return Result{Stdout: string(data)}, nil
	}
	if args[0] == "delete" {
		return Result{Stdout: `{}`}, nil
	}
	if args[0] == "secret" && len(args) > 2 && args[1] == "delete" {
		return Result{Stdout: `{"deleted":true}`}, nil
	}
	if args[0] == "scale" {
		f.deployed["resources"] = map[string]any{"cpu": args[3], "memory": args[5]}
		return Result{Stdout: `{}`}, nil
	}
	if args[0] == "validate" || args[0] == "deploy" || args[0] == "update" {
		path := args[2]
		if _, err := config.ReadProject(path); err != nil {
			return Result{}, err
		}
		if args[0] == "deploy" || args[0] == "update" {
			data, err := os.ReadFile(path)
			if err != nil {
				return Result{}, err
			}
			f.deployed, err = parseProject(data)
			if err != nil {
				return Result{}, err
			}
			image := "ghcr.io/acme/tool@sha256:" + strings.Repeat("a", 64)
			f.deployed["image"] = image
			f.deployed["source"].(map[string]any)["image"] = image
		}
		return Result{Stdout: `{}`}, nil
	}
	return Result{}, errors.New("unexpected CLI command")
}

func testClient(t *testing.T) (*Client, *fakeRunner, string) {
	t.Helper()
	root := t.TempDir()
	bundle := filepath.Join(root, "vendor.hostlier")
	if err := os.WriteFile(bundle, []byte(sampleBundle), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	client, err := Open(Options{BundlePath: bundle, StateDirectory: filepath.Join(root, "state"), Server: "production", Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	return client, runner, root
}

func TestCloudSDKPreservesBundleAcrossCustomerChanges(t *testing.T) {
	client, runner, root := testClient(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, CreateOptions{Name: "acme-app", Route: "acme.apps.example.com", Environment: map[string]string{"CUSTOMER_ID": "acme"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetCompute(ctx, "acme-app", Compute{CPU: "0.5", Memory: "512Mi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetEnvironment(ctx, "acme-app", map[string]string{"FEATURE_ENABLED": "true"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "state", "acme-app", ".hostlier"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := parseProject(data)
	if err != nil {
		t.Fatal(err)
	}
	if project["image"] != "ghcr.io/acme/tool@sha256:"+strings.Repeat("a", 64) || project["endpoint"].(map[string]any)["route"] != "acme.apps.example.com" || project["health"].(map[string]any)["path"] != "/health" || project["secrets"].(map[string]any)["API_KEY"] != "secret://vendor-key" || project["volumes"].(map[string]any)["data"].(map[string]any)["size"] != "5Gi" || project["resources"].(map[string]any)["cpu"] != "0.5" || project["env"].(map[string]any)["FEATURE_ENABLED"] != "true" {
		t.Fatalf("bundled contract was lost: %#v", project)
	}
	if _, err := config.ReadProject(filepath.Join(root, "state", "acme-app", ".hostlier")); err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, call := range runner.calls {
		commands = append(commands, call[0])
	}
	if strings.Join(commands, ",") != "validate,deploy,status,status,validate,scale,status,status,validate,update,status" {
		t.Fatal(commands)
	}
}

func TestCloudSDKInterruptedUpdateBlocksFurtherChanges(t *testing.T) {
	client, runner, root := testClient(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err != nil {
		t.Fatal(err)
	}
	runner.fail = "update"
	if _, err := client.SetEnvironment(ctx, "acme-app", map[string]string{"FEATURE_ENABLED": "true"}); err == nil {
		t.Fatal("update unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "state", "acme-app", "pending.json")); err != nil {
		t.Fatal("lost pending intent", err)
	}
	if _, err := client.SetCompute(ctx, "acme-app", Compute{CPU: "0.5", Memory: "512Mi"}); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatal("uncertain operation was retried", err)
	}
}

func TestCloudSDKRejectsChangedBundle(t *testing.T) {
	client, _, root := testClient(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "vendor.hostlier")
	if err := os.WriteFile(bundle, []byte(strings.Replace(sampleBundle, "1.0.0", "2.0.0", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	newer, err := Open(Options{BundlePath: bundle, StateDirectory: filepath.Join(root, "state"), Runner: client.runner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newer.SetEnvironment(ctx, "acme-app", map[string]string{"FEATURE_ENABLED": "true"}); err == nil || !strings.Contains(err.Error(), "bundle differs") {
		t.Fatal("accepted changed bundle", err)
	}
}

func TestCloudSDKBindsPerInstanceStoredSecrets(t *testing.T) {
	client, runner, root := testClient(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, CreateOptions{Name: "acme-app", Route: "acme.apps.example.com", Secrets: map[string]string{"API_KEY": "secret://acme-api-key"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "state", "acme-app", ".hostlier"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := parseProject(data)
	if err != nil || project["secrets"].(map[string]any)["API_KEY"] != "secret://acme-api-key" {
		t.Fatalf("per-instance binding was not saved: %v %#v", err, project)
	}
	if _, err := client.Create(ctx, CreateOptions{Name: "other-app", Route: "other.apps.example.com", Secrets: map[string]string{"UNKNOWN": "secret://other-key"}}); err == nil {
		t.Fatal("undeclared secret was accepted")
	}
	if _, err := client.Create(ctx, CreateOptions{Name: "third-app", Route: "third.apps.example.com", Secrets: map[string]string{"API_KEY": "plaintext"}}); err == nil {
		t.Fatal("plaintext secret was accepted")
	}
	if len(runner.calls) != 3 {
		t.Fatalf("invalid bindings reached the CLI: %d calls", len(runner.calls))
	}
}

func TestCloudSDKBindsSidecarStoredSecrets(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "vendor.hostlier")
	sidecar := `sidecars:
  storage:
    source:
      image: ghcr.io/acme/storage:1.0
    endpoint:
      exposure: loopback
      port: 9000
    secrets:
      ROOT_ACCESS_KEY_ID: secret://template-access
    health:
      exec: [test, -f, /tmp/ready]
`
	if err := os.WriteFile(bundle, []byte(sampleBundle+sidecar), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	client, err := Open(Options{BundlePath: bundle, StateDirectory: filepath.Join(root, "state"), Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Create(context.Background(), CreateOptions{Name: "acme-app", Route: "acme.apps.example.com", SidecarSecrets: map[string]map[string]string{"storage": {"ROOT_ACCESS_KEY_ID": "secret://acme-storage-access"}}}); err != nil {
		t.Fatal(err)
	}
	sidecars := runner.deployed["sidecars"].(map[string]any)
	storage := sidecars["storage"].(map[string]any)
	if storage["secrets"].(map[string]any)["ROOT_ACCESS_KEY_ID"] != "secret://acme-storage-access" {
		t.Fatal("sidecar secret binding was not applied")
	}
	if _, err := client.Create(context.Background(), CreateOptions{Name: "other-app", Route: "other.apps.example.com", SidecarSecrets: map[string]map[string]string{"unknown": {"ROOT_ACCESS_KEY_ID": "secret://key"}}}); err == nil {
		t.Fatal("undeclared sidecar was accepted")
	}
	if _, err := client.Create(context.Background(), CreateOptions{Name: "third-app", Route: "third.apps.example.com", SidecarSecrets: map[string]map[string]string{"storage": {"ROOT_ACCESS_KEY_ID": "plaintext"}}}); err == nil {
		t.Fatal("plaintext sidecar secret was accepted")
	}
	if len(runner.calls) != 3 {
		t.Fatalf("invalid sidecar binding reached the CLI: %d calls", len(runner.calls))
	}
}

func TestCloudSDKExpandsDeclaredSidecarVolumeWithoutChangingBundleIdentity(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "vendor.hostlier")
	sidecar := `sidecars:
  storage:
    source:
      image: ghcr.io/acme/storage:1.0
    endpoint:
      exposure: loopback
      port: 9000
    health:
      exec: [test, -f, /tmp/ready]
    volumes:
      data:
        mount: /data
        size: 1Gi
`
	if err := os.WriteFile(bundle, []byte(sampleBundle+sidecar), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	client, err := Open(Options{BundlePath: bundle, StateDirectory: filepath.Join(root, "state"), Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.Create(ctx, CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err != nil {
		t.Fatal(err)
	}
	before := len(runner.calls)
	for _, size := range []string{"512Mi", "1Gi", "garbage"} {
		if _, err := client.ExpandSidecarVolume(ctx, "acme-app", "storage", "data", size); err == nil {
			t.Fatal("non-expansion was accepted", size)
		}
	}
	if len(runner.calls) != before+3 { // Each attempted change checks the current controller spec.
		t.Fatal("invalid expansion submitted an update")
	}
	if _, err := client.ExpandSidecarVolume(ctx, "acme-app", "storage", "data", "10Gi"); err != nil {
		t.Fatal(err)
	}
	storage := runner.deployed["sidecars"].(map[string]any)["storage"].(map[string]any)
	if storage["volumes"].(map[string]any)["data"].(map[string]any)["size"] != "10Gi" {
		t.Fatal("expanded volume size missing from complete update")
	}
	if _, err := client.SetCompute(ctx, "acme-app", Compute{CPU: "0.5", Memory: "512Mi"}); err != nil {
		t.Fatal("original bundle identity lost after expansion", err)
	}
}

func TestCloudSDKPassesPrivateSecretInputAndControllerError(t *testing.T) {
	client, runner, root := testClient(t)
	value := []byte("private-secret-marker")
	if err := client.SetSecret(context.Background(), "acme-key", value); err != nil {
		t.Fatal(err)
	}
	if strings.Join(runner.secretArgs, ",") != "secret,set,acme-key,--stdin,--server,production" || string(runner.secretInput) != string(value) {
		t.Fatal("secret was not supplied through CLI stdin")
	}
	if strings.Contains(strings.Join(runner.secretArgs, " "), string(value)) {
		t.Fatal("secret entered CLI arguments")
	}
	if entries, err := os.ReadDir(filepath.Join(root, "state")); err == nil && len(entries) != 0 {
		t.Fatal("secret write created SDK state")
	}
	runner.fail = "secret"
	if err := client.SetSecret(context.Background(), "acme-key", value); err == nil || err.Error() != "APPROVAL_LIMIT: Too many retained approval requests" {
		t.Fatal("secret write discarded the controller error", err)
	}
	if err := client.SetSecret(context.Background(), "bad-key", nil); err == nil {
		t.Fatal("empty secret was accepted")
	}
}

func TestCloudSDKDeletesStoredSecretThroughExistingCLI(t *testing.T) {
	client, runner, _ := testClient(t)
	if err := client.DeleteSecret(context.Background(), "acme-s3-access"); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls[len(runner.calls)-1]; strings.Join(got, " ") != "secret delete acme-s3-access --server production" {
		t.Fatal("unexpected secret deletion command", got)
	}
	if err := client.DeleteSecret(context.Background(), "bad/name"); err == nil {
		t.Fatal("invalid secret name reached CLI")
	}
}

func TestCloudSDKRegistersInstanceSecretThroughPrivateCLIStdin(t *testing.T) {
	client, runner, _ := testClient(t)
	if err := client.SetInstanceSecret(context.Background(), "acme-app", "acme-app-key", []byte("private-value")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(runner.secretArgs, " "); got != "secret set acme-app-key --stdin --app acme-app --server production" {
		t.Fatal("instance secret owner was not sent to CLI", got)
	}
	if err := client.SetInstanceSecret(context.Background(), "bad/name", "acme-app-key", []byte("private-value")); err == nil {
		t.Fatal("invalid owning app reached CLI")
	}
}

func TestCloudSDKRecoversOnlyReviewedReadyPendingDeployment(t *testing.T) {
	client, runner, root := testClient(t)
	runner.fail = "deploy"
	if _, err := client.Create(context.Background(), CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err == nil {
		t.Fatal("expected interrupted deployment")
	}
	data, err := os.ReadFile(filepath.Join(root, "state", "acme-app", "pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pending struct {
		Project map[string]any `json:"project"`
	}
	if err := json.Unmarshal(data, &pending); err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/acme/tool@sha256:" + strings.Repeat("a", 64)
	runner.deployed = pending.Project
	runner.deployed["image"] = image
	runner.deployed["source"].(map[string]any)["image"] = image
	runner.fail = ""
	if err := client.RecoverPending(context.Background(), "acme-app", image); err == nil {
		t.Fatal("unready application was reconciled")
	}
	runner.statusReady = true
	if err := client.RecoverPending(context.Background(), "acme-app", "ghcr.io/acme/tool@sha256:"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("wrong image digest was reconciled")
	}
	if err := client.RecoverPending(context.Background(), "acme-app", image); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "acme-app", "pending.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending intent was not cleared after exact recovery", err)
	}
	status, err := client.StatusSnapshot(context.Background(), "acme-app")
	if err != nil || !status.Ready() || status.URL != "https://acme.apps.example.com" {
		t.Fatalf("typed readiness lost: %#v %v", status, err)
	}
}

func TestCloudSDKPurgeRequiresExactSucceededControllerOperation(t *testing.T) {
	client, runner, root := testClient(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err != nil {
		t.Fatal(err)
	}
	runner.fail = "delete"
	if _, err := client.Purge(ctx, "acme-app"); err == nil {
		t.Fatal("interrupted purge unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "state", "acme-app", "pending.json")); err != nil {
		t.Fatal("purge intent was lost", err)
	}
	runner.fail = ""
	runner.operation = map[string]any{"id": "op-delete", "kind": "action", "name": "acme-app", "action": "delete", "status": "succeeded", "purge": false}
	if err := client.ConfirmPurge(ctx, "acme-app", "op-delete"); err == nil {
		t.Fatal("retained-data deletion was mistaken for purge")
	}
	runner.operation["purge"] = true
	runner.deployed = nil
	if err := client.ConfirmPurge(ctx, "acme-app", "op-delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "acme-app", "retired.json")); err != nil {
		t.Fatal("retired marker missing", err)
	}
	if _, err := client.Create(ctx, CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err == nil {
		t.Fatal("purged name was reused without a new identity")
	}
}

func TestCloudSDKRecoversInterruptedPurgeFromNewControllerOperation(t *testing.T) {
	client, runner, root := testClient(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err != nil {
		t.Fatal(err)
	}
	runner.fail = "delete"
	if _, err := client.Purge(ctx, "acme-app"); err == nil {
		t.Fatal("interrupted purge unexpectedly succeeded")
	}
	runner.fail = ""
	runner.operation = map[string]any{"id": "op-delete", "kind": "action", "name": "acme-app", "action": "delete", "status": "succeeded", "purge": true}
	if err := client.RecoverPendingPurge(ctx, "acme-app"); err == nil {
		t.Fatal("recovery accepted operation while app still exists")
	}
	runner.deployed = nil
	if err := client.RecoverPendingPurge(ctx, "acme-app"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "acme-app", "retired.json")); err != nil {
		t.Fatal("retired marker missing", err)
	}
}

func TestCloudSDKRetriesPurgeOnlyWhenControllerAcceptedNoAction(t *testing.T) {
	client, runner, root := testClient(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, CreateOptions{Name: "acme-app", Route: "acme.apps.example.com"}); err != nil {
		t.Fatal(err)
	}
	runner.fail = "delete"
	if _, err := client.Purge(ctx, "acme-app"); err == nil {
		t.Fatal("interrupted purge unexpectedly succeeded")
	}
	runner.fail = ""
	if err := client.RecoverPendingPurge(ctx, "acme-app"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "acme-app", "retired.json")); err != nil {
		t.Fatal("safe retry did not retire app", err)
	}
}
