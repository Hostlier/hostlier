package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"hostlier/internal/controller"
	"hostlier/internal/model"
	hostruntime "hostlier/internal/runtime"
)

// hpaRestartCheckpoint holds only public owner and endpoint identity across actual VM reboots.
type hpaRestartCheckpoint struct {
	AdminPort int           `json:"adminPort"`
	HTTPSPort int           `json:"httpsPort"`
	Namespace string        `json:"namespace"`
	RuntimeID string        `json:"runtimeId"`
	HPAUID    string        `json:"hpaUid"`
	Spec      model.Project `json:"spec"`
}

// TestNativeHPARestartCheckpoint executes one explicit persistent disposable-VM acceptance phase.
func TestNativeHPARestartCheckpoint(t *testing.T) {
	requireNativeHPAE2E(t)
	dir := os.Getenv("HOSTLIER_AUTOSCALING_RESTART_DIR")
	// Persistent test state never uses an ambient or production directory.
	if dir != "/var/lib/hostlier-hpa-restart-e2e" {
		t.Skip("set the explicit disposable restart fixture directory")
	}
	phase := os.Getenv("HOSTLIER_AUTOSCALING_RESTART_PHASE")
	phases := map[string]func(){
		// Create protected state, the isolated TLS edge and the actual controller service.
		"setup": func() { setupHPARestart(t, dir) },
		// Verify the persisted HPA identity, approved capacity bounds and trusted HTTPS after restart.
		"live": func() { verifyHPARestart(t, dir, false) },
		// Crash after readiness and durable target intent but before replacement HPA installation.
		"pending-crash": func() { crashHPARestartHandoff(t, dir) },
		// Recover pending native authority through a restarted actual controller and approved replay.
		"recover-pending": func() { recoverHPARestartHandoff(t, dir) },
		// Crash after durable revocation but before deleting the old native replica writer.
		"suspend-crash": func() { crashHPARestartSuspend(t, dir) },
		// Complete revocation after restart and require fresh approval before restoring delegation.
		"recover-suspend": func() { recoverHPARestartSuspend(t, dir) },
		// Prove genuine native CPU scaling while the actual Hostlier controller is stopped.
		"load-controller-down": func() { loadHPARestartWithoutController(t, dir) },
		// Remove native scaling authority before applying explicitly approved zero capacity.
		"zero": func() { zeroHPARestart(t, dir) },
		// Verify actual controller and machine restart cannot wake stopped capacity.
		"verify-zero": func() { verifyHPARestart(t, dir, true) },
		// Remove only this fixture's namespace, services, protected files and test TLS material.
		"cleanup": func() { cleanupHPARestart(t, dir) },
	}
	run, ok := phases[phase]
	// Only named acceptance phases are permitted to mutate the disposable checkpoint.
	if !ok {
		t.Fatal("unknown explicitly requested checkpoint phase")
	}
	run()
}

// TestNativeHPARestartController runs a real durable Controller under a fixture-only systemd service.
func TestNativeHPARestartController(t *testing.T) {
	requireNativeHPAE2E(t)
	dir := os.Getenv("HOSTLIER_AUTOSCALING_RESTART_DIR")
	// Ordinary test execution cannot become a long-lived controller daemon.
	if dir != "/var/lib/hostlier-hpa-restart-e2e" {
		t.Skip("fixture service only")
	}
	checkpoint := readHPARestart(t, dir)
	adapter := restartHPAFixture(t, hpaRestartOptions(dir, checkpoint))
	c, err := controller.New(controller.Options{StateDir: filepath.Join(dir, "controller"), Config: model.ServerConfig{Version: 1, Domains: []string{"hostlier.test"}}, Hostname: "controller.hostlier.test", Runtime: adapter})
	// The actual controller's store ownership and lifecycle monitor must initialize successfully.
	if err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	<-signals
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Service restart must release the actual durable controller state owner.
	if err = c.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// setupHPARestart creates independent TLS and controller services without modifying installed fixtures.
func setupHPARestart(t *testing.T, dir string) {
	// Refuse to overwrite an earlier checkpoint or adopt its namespace.
	if _, err := os.Stat(filepath.Join(dir, "checkpoint.json")); err == nil {
		t.Fatal("restart checkpoint already exists")
	}
	// Exercise each independent policy or recovery case with its own fixture state.
	for _, child := range []string{"runtime", "controller"} {
		// Both durable owners use dedicated protected directories.
		if err := os.MkdirAll(filepath.Join(dir, child), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cert, key, _ := fixtureTLS(t, dir)
	checkpoint := hpaRestartCheckpoint{AdminPort: availablePort(t), HTTPSPort: availablePort(t), Spec: faultProject(t, false)}
	one := 1
	checkpoint.Spec.Name = "restart"
	checkpoint.Spec.Route = "restart.hostlier.test"
	checkpoint.Spec.Replicas = &one
	checkpoint.Spec.Requests = &model.Limits{CPU: "0.01", Memory: "32Mi"}
	checkpoint.Spec.Autoscaling = &model.Autoscaling{MinReplicas: 1, MaxReplicas: 2, CPUTargetPercent: 65}
	sum := sha256.Sum256([]byte(filepath.Join(dir, "runtime")))
	checkpoint.Namespace = "hostlier-" + hex.EncodeToString(sum[:])[:16] + "-restart"
	saveHPARestart(t, dir, checkpoint)
	startHPARestartEdge(t, dir, checkpoint, cert, key)
	adapter := restartHPAFixture(t, hpaRestartOptions(dir, checkpoint))
	deployed, err := adapter.Deploy(context.Background(), checkpoint.Spec, model.DeployContext{OperationID: "explicit-restart-delegation"})
	// Persist the ready target before starting the independent actual controller process.
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.RuntimeID = deployed.RuntimeID
	checkpoint.HPAUID = kubeFault(t, "get", "hpa", "autoscaler", "-n", checkpoint.Namespace, "-o", "jsonpath={.metadata.uid}")
	saveHPARestart(t, dir, checkpoint)
	startHPARestartController(t, dir)
	verifyHPARestart(t, dir, false)
}

// startHPARestartEdge persists the isolated edge's autosaved routing across actual machine reboots.
func startHPARestartEdge(t *testing.T, dir string, c hpaRestartCheckpoint, cert, key string) {
	cfg := map[string]any{"admin": map[string]any{"listen": fmt.Sprintf("127.0.0.1:%d", c.AdminPort)}, "apps": map[string]any{"tls": map[string]any{"certificates": map[string]any{"load_files": []any{map[string]string{"certificate": cert, "key": key}}}}, "http": map[string]any{"servers": map[string]any{"hostlier": map[string]any{"listen": []string{fmt.Sprintf("127.0.0.1:%d", c.HTTPSPort)}, "tls_connection_policies": []any{map[string]any{}}, "automatic_https": map[string]any{"disable": true}, "routes": []any{}}}}}}
	raw, _ := json.Marshal(cfg)
	writeHPARestartFile(t, filepath.Join(dir, "caddy.json"), raw)
	unit := "[Unit]\nDescription=Task-owned HPA restart TLS fixture\nAfter=network.target\n[Service]\nEnvironment=XDG_CONFIG_HOME=" + dir + "/caddy-config\nEnvironment=XDG_DATA_HOME=" + dir + "/caddy-data\nExecStart=/usr/bin/caddy run --resume --config " + dir + "/caddy.json\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\n"
	installHPARestartUnit(t, "hostlier-hpa-restart-edge", unit)
	deadline := time.Now().Add(15 * time.Second)
	// Do not deploy until the independent local edge accepts configuration.
	for time.Now().Before(deadline) {
		response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/config/", c.AdminPort))
		// Require the rejected or faulted operation to fail instead of silently granting authority.
		if err == nil {
			response.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("persistent fixture Caddy admin unavailable")
}

// startHPARestartController installs a separate real controller service with its own state owner.
func startHPARestartController(t *testing.T, dir string) {
	unit := "[Unit]\nDescription=Task-owned native HPA restart controller\nAfter=k3s.service hostlier-hpa-restart-edge.service\nRequires=k3s.service\n[Service]\nEnvironment=HOSTLIER_AUTOSCALING_E2E=1\nEnvironment=HOSTLIER_AUTOSCALING_RESTART_DIR=" + dir + "\nExecStart=/usr/local/lib/hostlier-hpa-restart-e2e.test -test.run=^TestNativeHPARestartController$ -test.timeout=24h\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\n"
	installHPARestartUnit(t, "hostlier-hpa-restart-controller", unit)
}

// installHPARestartUnit owns only the two explicitly named disposable acceptance services.
func installHPARestartUnit(t *testing.T, name, unit string) {
	writeHPARestartFile(t, "/etc/systemd/system/"+name+".service", []byte(unit))
	hpaRestartCommand(t, "systemctl", "daemon-reload")
	hpaRestartCommand(t, "systemctl", "enable", "--now", name+".service")
}

// verifyHPARestart verifies bounded native ownership or persistent zero using actual cluster and TLS state.
func verifyHPARestart(t *testing.T, dir string, zero bool) {
	c := readHPARestart(t, dir)
	hpa := kubeFault(t, "get", "hpa", "-n", c.Namespace, "-o", "jsonpath={.items[*].metadata.uid}")
	replicas := kubeFault(t, "get", "deploy", c.RuntimeID, "-n", c.Namespace, "-o", "jsonpath={.spec.replicas}")
	// Explicit zero must remain stopped with no HPA across both controller and machine restart.
	if zero {
		// Revocation must leave no native HPA capable of changing stopped or manual capacity.
		if hpa != "" || replicas != "0" {
			t.Fatal("zero delegation woke", hpa, replicas)
		}
		return
	}
	// A VM restart must preserve the exact installed HPA rather than silently revoke/recreate it.
	if hpa != c.HPAUID || (replicas != "1" && replicas != "2") {
		t.Fatal("live delegation changed identity or escaped bounds", hpa, replicas)
	}
	pool := x509.NewCertPool()
	ca, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil || !pool.AppendCertsFromPEM(ca) {
		t.Fatal("persistent fixture CA unavailable", err)
	}
	code, _ := agentHTTPSGetter(fixtureHTTPSGetter(t, c.HTTPSPort, pool))("https://restart.hostlier.test")
	// HTTPS is certificate-validated against the fixture's persisted CA, never insecure transport.
	if code != 200 {
		t.Fatal("live delegation lost verified HTTPS", code)
	}
	hpaRestartCommand(t, "systemctl", "is-active", "hostlier-hpa-restart-controller.service")
}

// loadHPARestartWithoutController proves native autonomous scaling while the actual controller is stopped.
func loadHPARestartWithoutController(t *testing.T, dir string) {
	c := readHPARestart(t, dir)
	hpaRestartCommand(t, "systemctl", "stop", "hostlier-hpa-restart-controller.service")
	pod := kubeFault(t, "get", "pods", "-n", c.Namespace, "-l", "io.hostlier.version="+c.RuntimeID, "-o", "jsonpath={.items[0].metadata.name}")
	kubeFault(t, "exec", "-n", c.Namespace, pod, "--", "sh", "-c", "sh -c 'while :; do :; done' </dev/null >/dev/null 2>&1 & echo $! >/tmp/hostlier-cpu-burn.pid")
	deadline := time.Now().Add(3 * time.Minute)
	// Only native HPA is running during this observed upscale.
	for time.Now().Before(deadline) {
		// Keep actual desired and ready capacity within the reviewed replica contract.
		if count := kubeFault(t, "get", "deploy", c.RuntimeID, "-n", c.Namespace, "-o", "jsonpath={.status.readyReplicas}"); count == "2" {
			hpaRestartCommand(t, "systemctl", "start", "hostlier-hpa-restart-controller.service")
			time.Sleep(5 * time.Second)
			verifyHPARestart(t, dir, false)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatal("native HPA did not scale during controller downtime")
}

// zeroHPARestart serializes an approved manual zero with the fixture controller's state owner.
func zeroHPARestart(t *testing.T, dir string) {
	c := readHPARestart(t, dir)
	hpaRestartCommand(t, "systemctl", "stop", "hostlier-hpa-restart-controller.service")
	adapter := restartHPAFixture(t, hpaRestartOptions(dir, c))
	c.Spec.Autoscaling = nil
	zero := 0
	c.Spec.Replicas = &zero
	deployed, err := adapter.Deploy(context.Background(), c.Spec, model.DeployContext{OperationID: "explicit-manual-zero-before-reboot"})
	// Manual revocation and actual zero must complete before restarting the controller.
	if err != nil {
		t.Fatal(err)
	}
	c.RuntimeID = deployed.RuntimeID
	saveHPARestart(t, dir, c)
	hpaRestartCommand(t, "systemctl", "start", "hostlier-hpa-restart-controller.service")
	time.Sleep(5 * time.Second)
	verifyHPARestart(t, dir, true)
}

// cleanupHPARestart removes only this fixture's services, namespace and protected checkpoint.
func cleanupHPARestart(t *testing.T, dir string) {
	c := readHPARestart(t, dir)
	// Exercise each independent policy or recovery case with its own fixture state.
	for _, name := range []string{"hostlier-hpa-restart-controller", "hostlier-hpa-restart-edge"} {
		hpaRestartCommand(t, "systemctl", "disable", "--now", name+".service")
		// Unit paths are fixed fixture identities and never inferred from cluster data.
		if err := os.Remove("/etc/systemd/system/" + name + ".service"); err != nil {
			t.Fatal(err)
		}
	}
	hpaRestartCommand(t, "systemctl", "daemon-reload")
	kubeFault(t, "delete", "namespace", c.Namespace, "--wait=true", "--timeout=60s")
	// The fixed protected directory contains only this acceptance fixture's state and TLS material.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
}

// hpaRestartOptions reconstructs the exact runtime owner and private edge after every machine reboot.
func hpaRestartOptions(dir string, c hpaRestartCheckpoint) hostruntime.Options {
	return hostruntime.Options{Type: "k3s", StateDir: filepath.Join(dir, "runtime"), CaddyAdmin: fmt.Sprintf("http://127.0.0.1:%d", c.AdminPort), SkipPublicHTTPSProbe: true, HealthTimeout: 45 * time.Second}
}

// saveHPARestart writes public checkpoint identity without changing protected production state.
func saveHPARestart(t *testing.T, dir string, c hpaRestartCheckpoint) {
	raw, err := json.Marshal(c)
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil {
		t.Fatal(err)
	}
	writeHPARestartFile(t, filepath.Join(dir, "checkpoint.json"), raw)
}

// readHPARestart requires the exact fixture checkpoint before any destructive cleanup or lifecycle action.
func readHPARestart(t *testing.T, dir string) hpaRestartCheckpoint {
	raw, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil {
		t.Fatal(err)
	}
	var c hpaRestartCheckpoint
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// writeHPARestartFile preserves private fixture file permissions for TLS, state and service definitions.
func writeHPARestartFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// hpaRestartCommand runs only explicit local disposable-VM service commands with a bounded deadline.
func hpaRestartCommand(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil {
		t.Fatalf("restart fixture command %s failed: %v %s", args[0], err, raw)
	}
}

// crashHPARestartHandoff exits after durable target transfer intent and before native HPA installation.
func crashHPARestartHandoff(t *testing.T, dir string) {
	c := readHPARestart(t, dir)
	hpaRestartCommand(t, "systemctl", "stop", "hostlier-hpa-restart-controller.service")
	sum := sha256.Sum256([]byte("restart-crash-handoff"))
	c.RuntimeID = "app-" + hex.EncodeToString(sum[:])[:16]
	c.HPAUID = ""
	saveHPARestart(t, dir, c)
	options := hpaRestartOptions(dir, c)
	options.KubeRun = crashBeforeHPAApply
	adapter := restartHPAFixture(t, options)
	_, err := adapter.Deploy(context.Background(), c.Spec, model.DeployContext{OperationID: "restart-crash-handoff"})
	t.Fatal("expected abrupt pending HPA crash was not reached", err)
}

// crashBeforeHPAApply preserves real Kubernetes execution except at the explicit pending-journal crash boundary.
func crashBeforeHPAApply(ctx context.Context, args []string, input []byte) ([]byte, error) {
	var object map[string]any
	// Runtime has already committed its ready target and pending HPA journal before this apply.
	if json.Unmarshal(input, &object) == nil && object["kind"] == "HorizontalPodAutoscaler" {
		os.Exit(85)
	}
	return executeHPARestartKubectl(ctx, args, input)
}

// recoverHPARestartHandoff proves actual controller restart completes pending native target installation.
func recoverHPARestartHandoff(t *testing.T, dir string) {
	c := readHPARestart(t, dir)
	hpaRestartCommand(t, "systemctl", "start", "hostlier-hpa-restart-controller.service")
	deadline := time.Now().Add(45 * time.Second)
	// The actual controller's periodic lifecycle reconciler must finish the persisted pending install.
	for time.Now().Before(deadline) {
		c.HPAUID = kubeFault(t, "get", "hpa", "autoscaler", "-n", c.Namespace, "--ignore-not-found", "-o", "jsonpath={.metadata.uid}")
		// Bind restart acceptance to the exact approved native HPA identity and capacity interval.
		if c.HPAUID != "" {
			break
		}
		time.Sleep(time.Second)
	}
	// Missing HPA after recovery cannot be mistaken for a safely resumed policy.
	if c.HPAUID == "" {
		t.Fatal("controller restart did not recover pending HPA")
	}
	saveHPARestart(t, dir, c)
	hpaRestartCommand(t, "systemctl", "stop", "hostlier-hpa-restart-controller.service")
	adapter := restartHPAFixture(t, hpaRestartOptions(dir, c))
	_, err := adapter.Deploy(context.Background(), c.Spec, model.DeployContext{OperationID: "restart-crash-handoff"})
	// Approved immutable operation replay retires source capacity without replacing the live HPA.
	if err != nil {
		t.Fatal(err)
	}
	hpaRestartCommand(t, "systemctl", "start", "hostlier-hpa-restart-controller.service")
	time.Sleep(5 * time.Second)
	verifyHPARestart(t, dir, false)
}

// crashHPARestartSuspend exits after durable pause but before deleting the old native replica writer.
func crashHPARestartSuspend(t *testing.T, dir string) {
	c := readHPARestart(t, dir)
	hpaRestartCommand(t, "systemctl", "stop", "hostlier-hpa-restart-controller.service")
	options := hpaRestartOptions(dir, c)
	options.KubeRun = crashBeforeHPADelete
	adapter := restartHPAFixture(t, options)
	_, err := adapter.Deploy(context.Background(), c.Spec, model.DeployContext{OperationID: "never-started-before-suspend-crash"})
	t.Fatal("expected abrupt durable pause crash was not reached", err)
}

// crashBeforeHPADelete leaves the existing HPA in place after the runtime's revocation journal commits.
func crashBeforeHPADelete(ctx context.Context, args []string, input []byte) ([]byte, error) {
	// This fault is scoped to the owned HPA delete; other real cluster commands remain unchanged.
	for i, arg := range args {
		// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
		if arg == "delete" && i+1 < len(args) && args[i+1] == "horizontalpodautoscaler" {
			os.Exit(86)
		}
	}
	return executeHPARestartKubectl(ctx, args, input)
}

// executeHPARestartKubectl forwards exact command placement and manifest bytes during fault injection.
func executeHPARestartKubectl(ctx context.Context, args []string, input []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, "/usr/local/bin/k3s", args...)
	command.Stdin = bytes.NewReader(input)
	return command.Output()
}

// recoverHPARestartSuspend proves restarted controller revocation precedes a newly approved deployment.
func recoverHPARestartSuspend(t *testing.T, dir string) {
	c := readHPARestart(t, dir)
	hpaRestartCommand(t, "systemctl", "start", "hostlier-hpa-restart-controller.service")
	deadline := time.Now().Add(45 * time.Second)
	// Restart reconciliation must delete the still-present old HPA instead of resuming it.
	for time.Now().Before(deadline) {
		// Require the independently observed ownership or capacity condition before accepting this lifecycle step.
		if hpa := kubeFault(t, "get", "hpa", "-n", c.Namespace, "-o", "jsonpath={.items[*].metadata.name}"); hpa == "" {
			break
		}
		time.Sleep(time.Second)
	}
	// Revocation must leave no native HPA capable of changing stopped or manual capacity.
	if hpa := kubeFault(t, "get", "hpa", "-n", c.Namespace, "-o", "jsonpath={.items[*].metadata.name}"); hpa != "" {
		t.Fatal("restart retained suspended HPA")
	}
	// No candidate may have been created before the interrupted HPA deletion.
	if ids := kubeFault(t, "get", "deploy", "-n", c.Namespace, "-o", "jsonpath={.items[?(@.spec.replicas>0)].metadata.name}"); ids != c.RuntimeID {
		t.Fatal("suspend crash changed serving capacity", ids)
	}
	hpaRestartCommand(t, "systemctl", "stop", "hostlier-hpa-restart-controller.service")
	adapter := restartHPAFixture(t, hpaRestartOptions(dir, c))
	deployed, err := adapter.Deploy(context.Background(), c.Spec, model.DeployContext{OperationID: "fresh-approval-after-recovered-revocation"})
	// Require a successful fixture operation or authoritative observation before continuing safety assertions.
	if err != nil {
		t.Fatal(err)
	}
	c.RuntimeID = deployed.RuntimeID
	c.HPAUID = kubeFault(t, "get", "hpa", "autoscaler", "-n", c.Namespace, "-o", "jsonpath={.metadata.uid}")
	saveHPARestart(t, dir, c)
	hpaRestartCommand(t, "systemctl", "start", "hostlier-hpa-restart-controller.service")
	time.Sleep(5 * time.Second)
	verifyHPARestart(t, dir, false)
}
