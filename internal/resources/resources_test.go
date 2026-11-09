package resources

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
)

// TestPlanIsReadOnlyAndRejectsUnsupportedChanges checks immutable read-only plans and invalid resource requests.
func TestPlanIsReadOnlyAndRejectsUnsupportedChanges(t *testing.T) {
	dir := t.TempDir()
	manager, err := New(Options{StateDir: dir})
	// Require the selected resource backend to initialize successfully.
	if err != nil {
		t.Fatal(err)
	}
	spec := model.ResourceSpec{Name: "database", Type: "postgres", Storage: "1Gi"}
	plan, err := manager.Plan(context.Background(), spec)
	// Require a valid read-only resource plan before using its normalized specification.
	if err != nil {
		t.Fatal(err)
	}
	// Fail the fixture on image not immutable.
	if !strings.Contains(plan.Spec.Image, "@sha256:") {
		t.Fatal("image not immutable")
	}
	files, _ := os.ReadDir(dir)
	// Fail the fixture on planning changed state.
	if len(files) != 0 {
		t.Fatal("planning changed state")
	}
	// Reject every unsupported resource request without changing state.
	for _, bad := range []model.ResourceSpec{{Name: "db", Type: "mysql", Storage: "1Gi"}, {Name: "db", Type: "postgres", Storage: "0Gi"}, {Name: "db", Type: "postgres", Storage: "999999999999999999999999Gi"}, {Name: "db", Type: "postgres", Storage: "1Gi", Image: "postgres:latest"}, {Name: "../db", Type: "redis", Storage: "1Gi"}} {
		// Fail the fixture on accepted unsupported spec %#v.
		if _, err = manager.Plan(context.Background(), bad); err == nil {
			t.Fatalf("accepted unsupported spec %#v", bad)
		}
	}
}

// TestDockerMultiplexKeepsBinaryBackup checks binary-safe Docker output decoding and truncated-stream rejection.
func TestDockerMultiplexKeepsBinaryBackup(t *testing.T) {
	payload := []byte{0, 255, 1, 2, 3}
	header := make([]byte, 8)
	header[0] = 1
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	stdout, stderr, err := demultiplex(append(header, payload...))
	// Fail the fixture on binary output corrupted.
	if err != nil || !bytes.Equal(stdout, payload) || len(stderr) != 0 {
		t.Fatal("binary output corrupted")
	}
	// Fail the fixture on truncated stream accepted.
	if _, _, err = demultiplex(append(header, 1)); err == nil {
		t.Fatal("truncated stream accepted")
	}
}

// TestLiveDockerResourceProtocolsAndPersistence checks real engine authentication, persistence and independent native restores.
func TestLiveDockerResourceProtocolsAndPersistence(t *testing.T) {
	// Require explicit opt-in before running disposable live infrastructure tests.
	if os.Getenv("HOSTLIER_LIVE_RESOURCES") != "1" {
		t.Skip("set HOSTLIER_LIVE_RESOURCES=1 for owned disposable Docker database tests")
	}
	// Apply the same contract to each supported engine or owned object kind.
	for _, kind := range []string{"postgres", "redis", "neo4j"} {
		// Run the same engine contract independently for this backend.
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			// Retain diagnostic context while keeping fixture credentials private.
			generic, err := New(Options{StateDir: t.TempDir(), HealthTimeout: 2 * time.Minute, OnDiagnostic: func(value string) { t.Log("protected maintenance diagnostics:", value) }})
			// Require the selected resource backend to initialize successfully.
			if err != nil {
				t.Fatal(err)
			}
			manager := generic.(*Manager)
			name := "fixture"
			credentials := model.ResourceCredentials{Username: "hostlier", Password: "fixture-aB7f53b9e0334e4da40d", Database: "app"}
			// Use the Neo4j image authentication username for this fixture.
			if kind == "neo4j" {
				credentials.Username = "neo4j"
			}
			operation := model.ResourceContext{Credentials: credentials, Purge: true}
			// Purge this test resource with its own credentials when its verification scope exits.
			defer func() {
				// Keep failure diagnostics available without masking the original operation result.
				if err := manager.Action(context.Background(), name, "delete", operation); err != nil {
					t.Log("cleanup:", err)
				}
			}()
			spec := model.ResourceSpec{Name: name, Type: kind, Storage: "1Gi"}
			item, err := manager.Provision(ctx, spec, operation)
			// Keep failure diagnostics available without masking the original operation result.
			if err != nil {
				// Redact and emit provisioning diagnostics only when log retrieval succeeds.
				if log, e := manager.Logs(ctx, name); e == nil {
					t.Log(strings.ReplaceAll(log, credentials.Password, "[REDACTED]"))
				}
				t.Fatal(err)
			}
			// Fail the fixture on resource not running.
			if item.Status != "running" {
				t.Fatal("resource not running")
			}

			checkDockerResourceIsolation(t, ctx, manager, name, item)
			write, read, env := dockerFixtureCommands(kind, credentials)
			checkDockerResourceAuthentication(t, ctx, manager, name, operation, write, read, env)
			checkDockerResourceRetry(t, ctx, manager, name, credentials, operation, spec, item)
			checkDockerResourceRestore(t, ctx, manager, name, kind, credentials, operation, spec, read)
			checkDockerResourceRetainedData(t, ctx, manager, name, credentials, operation, spec, item, read, env)
		})
	}
}

// TestRestoreRefusesMissingAcceptedOperation checks that unapproved restore fails before resource lookup.
func TestRestoreRefusesMissingAcceptedOperation(t *testing.T) {
	generic, err := New(Options{StateDir: t.TempDir()})
	// Require the selected resource backend to initialize successfully.
	if err != nil {
		t.Fatal(err)
	}
	manager := generic.(*Manager)
	// Fail the fixture on restore without accepted operation was allowed.
	if err = manager.Restore(context.Background(), "uncreated", []byte("backup"), model.ResourceContext{}); !code(err, "APPROVAL_REQUIRED") {
		t.Fatal("restore without accepted operation was allowed", err)
	}
}

// checkDockerResourceIsolation checks unpublished database ports and the internal network.
func checkDockerResourceIsolation(t *testing.T, ctx context.Context, manager *Manager, name string, item model.Resource) {

	t.Helper()
	var err error
	inspected, err := manager.inspect(ctx, name)
	// Require a verified owned container before changing or reporting its state.
	if err != nil {
		t.Fatal(err)
	}
	// Inspect every published binding to ensure database ports remain private.
	for _, binding := range inspected.NetworkSettings.Ports {
		// Fail the fixture on database port published.
		if binding != nil {
			t.Fatalf("database port published: %#v", binding)
		}
	}
	var net struct{ Internal bool }
	// Fail the fixture on resource network is not internal.
	if _, err = manager.api(ctx, "GET", "/networks/"+item.Network, nil, &net); err != nil || !net.Internal {
		t.Fatal("resource network is not internal")
	}
}

// dockerFixtureCommands Builds authenticated native fixture write and read commands.
func dockerFixtureCommands(kind string, credentials model.ResourceCredentials) (write, read, env []string) {
	env = authEnv(kind, credentials)
	// Select the native behavior for the requested engine or lifecycle operation.
	switch kind {
	// Use PostgreSQL native commands and its authenticated database configuration.
	case "postgres":
		write = []string{"psql", "-h", "127.0.0.1", "-U", credentials.Username, "-d", "app", "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE fixture_data (value text); INSERT INTO fixture_data VALUES ('persisted-value')"}
		read = []string{"psql", "-h", "127.0.0.1", "-U", credentials.Username, "-d", "app", "-Atc", "SELECT value FROM fixture_data"}
	// Use Redis native commands and its authenticated persistence configuration.
	case "redis":
		write = []string{"redis-cli", "--user", credentials.Username, "SET", "fixture-key", "persisted-value"}
		read = []string{"redis-cli", "--user", credentials.Username, "GET", "fixture-key"}
	// Use Neo4j native commands and its authenticated database configuration.
	case "neo4j":
		write = []string{"cypher-shell", "-a", "bolt://127.0.0.1:7687", "-u", credentials.Username, "CREATE (:Fixture {value: 'persisted-value'});"}
		read = []string{"cypher-shell", "-a", "bolt://127.0.0.1:7687", "-u", credentials.Username, "MATCH (n:Fixture) RETURN n.value;"}
	}
	return write, read, env
}

// checkDockerResourceAuthentication checks password enforcement and persistence after restart.
func checkDockerResourceAuthentication(t *testing.T, ctx context.Context, manager *Manager, name string, operation model.ResourceContext, write, read, env []string) {

	t.Helper()
	var err error
	// Fail the fixture on authenticated write.
	if _, err = manager.execute(ctx, name, write, env); err != nil {
		t.Fatal("authenticated write:", err)
	}
	bad := append([]string{}, env...)
	bad[0] = strings.SplitN(bad[0], "=", 2)[0] + "=wrong-password"
	output, err := manager.execute(ctx, name, read, bad)
	// Fail the fixture on wrong password authorized read.
	if err == nil && strings.Contains(string(output), "persisted-value") {
		t.Fatal("wrong password authorized read")
	}
	// Fail the fixture on restart.
	if err = manager.Action(ctx, name, "restart", operation); err != nil {
		t.Fatal("restart:", err)
	}
	output, err = manager.execute(ctx, name, read, env)
	// Fail the fixture on persistence.
	if err != nil || !strings.Contains(string(output), "persisted-value") {
		t.Fatalf("persistence: %v %s", err, output)
	}
}

// checkDockerResourceRetry checks idempotent provisioning and protected connection metadata.
func checkDockerResourceRetry(t *testing.T, ctx context.Context, manager *Manager, name string, credentials model.ResourceCredentials, operation model.ResourceContext, spec model.ResourceSpec, item model.Resource) {

	t.Helper()
	again, err := manager.Provision(ctx, spec, operation)
	// Fail the fixture on retry created a different instance.
	if err != nil || again.RuntimeID != item.RuntimeID {
		t.Fatal("retry created a different instance", err)
	}
	metadata, _ := json.Marshal(again)
	// Fail the fixture on resource metadata leaked password.
	if strings.Contains(string(metadata), credentials.Password) {
		t.Fatal("resource metadata leaked password")
	}
	connection, err := manager.Connection(ctx, name, credentials)
	// Fail the fixture on connection binding.
	if err != nil || len(connection) == 0 {
		t.Fatal("connection binding:", err)
	}
}

// checkDockerResourceRestore checks native backup into an independently authenticated instance.
func checkDockerResourceRestore(t *testing.T, ctx context.Context, manager *Manager, name, kind string, credentials model.ResourceCredentials, operation model.ResourceContext, spec model.ResourceSpec, read []string) {

	t.Helper()
	var err error

	backup, backupErr := manager.Backup(ctx, name, operation)
	// Fail the fixture on native backup.
	if backupErr != nil {
		t.Fatal("native backup:", backupErr)
	}
	// Fail the fixture on empty backup.
	if len(backup) == 0 {
		t.Fatal("empty backup")
	}
	targetSpec := spec
	targetSpec.Name = "restored"
	targetCredentials := credentials
	targetCredentials.Password = "independent-target-cfdd3d60f5b84211"
	targetContext := model.ResourceContext{Credentials: targetCredentials, OperationID: "approved-restore-test", Purge: true}
	// Purge this test resource with its own credentials when its verification scope exits.
	defer func() {
		// Keep failure diagnostics available without masking the original operation result.
		if cleanupErr := manager.Action(context.Background(), targetSpec.Name, "delete", targetContext); cleanupErr != nil {
			t.Log("restore target cleanup:", cleanupErr)
		}
	}()
	// Fail the fixture on restore target provision.
	if _, err = manager.Provision(ctx, targetSpec, targetContext); err != nil {
		t.Fatal("restore target provision:", err)
	}
	// Fail the fixture on independent restore.
	if err = manager.Restore(ctx, targetSpec.Name, backup, targetContext); err != nil {
		t.Fatal("independent restore:", err)
	}
	checkDockerRestoredQuery(t, ctx, manager, kind, targetSpec.Name, read, targetCredentials, "independent restored query")
	// Fail the fixture on restore target restart.
	if err = manager.Action(ctx, targetSpec.Name, "restart", targetContext); err != nil {
		t.Fatal("restore target restart:", err)
	}
	checkDockerRestoredQuery(t, ctx, manager, kind, targetSpec.Name, read, targetCredentials, "restore restart persistence")
}

// checkDockerResourceRetainedData checks data retained across container deletion and recreation.
func checkDockerResourceRetainedData(t *testing.T, ctx context.Context, manager *Manager, name string, credentials model.ResourceCredentials, operation model.ResourceContext, spec model.ResourceSpec, item model.Resource, read, env []string) {

	t.Helper()
	var err error
	// Fail the fixture on retained delete.
	if err = manager.Action(ctx, name, "delete", model.ResourceContext{Credentials: credentials}); err != nil {
		t.Fatal("retained delete:", err)
	}
	restored, err := manager.Provision(ctx, spec, operation)
	// Fail the fixture on recreate retained data.
	if err != nil {
		t.Fatal("recreate retained data:", err)
	}
	// Fail the fixture on container was not recreated.
	if restored.RuntimeID == item.RuntimeID {
		t.Fatal("container was not recreated")
	}
	output, err := manager.execute(ctx, name, read, env)
	// Fail the fixture on retained data.
	if err != nil || !strings.Contains(string(output), "persisted-value") {
		t.Fatalf("retained data: %v %s", err, output)
	}
}

// checkDockerRestoredQuery verifies data using the restore target's independent credentials.
func checkDockerRestoredQuery(t *testing.T, ctx context.Context, manager *Manager, kind, name string, read []string, credentials model.ResourceCredentials, phase string) {
	t.Helper()
	output, err := manager.execute(ctx, name, read, authEnv(kind, credentials))
	// Require the restored native query to return the expected persisted value.
	if err != nil || !strings.Contains(string(output), "persisted-value") {
		t.Fatalf("%s: %v %s", phase, err, output)
	}
}
