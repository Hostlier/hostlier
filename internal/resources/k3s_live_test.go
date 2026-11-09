package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
)

// TestLiveK3sResources checks private authentication, persistence and independent restores on real k3s engines.
func TestLiveK3sResources(t *testing.T) {
	// Require explicit opt-in before running disposable live infrastructure tests.
	if os.Getenv("HOSTLIER_LIVE_K3S_RESOURCES") != "1" {
		t.Skip("set HOSTLIER_LIVE_K3S_RESOURCES=1 inside disposable single-node k3s VM")
	}
	// Apply the same contract to each supported engine or owned object kind.
	for _, kind := range []string{"postgres", "redis", "neo4j"} {
		// Run the same engine contract independently for this backend.
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			// Retain diagnostic context while keeping fixture credentials private.
			adapter, e := New(Options{Type: "k3s", StateDir: t.TempDir(), HealthTimeout: 3 * time.Minute, OnDiagnostic: func(value string) {
				value = strings.ReplaceAll(value, "fixture-private-password-a123", "[REDACTED]")
				value = strings.ReplaceAll(value, "restored-private-password-b456", "[REDACTED]")
				t.Log("protected diagnostic", value)
			}})
			// Require the selected resource backend to initialize successfully.
			if e != nil {
				t.Fatal(e)
			}
			m := adapter.(*K3sManager)
			c := model.ResourceCredentials{Username: "hostlier", Password: "fixture-private-password-a123", Database: "app"}
			// Use the Neo4j image authentication username for this fixture.
			if kind == "neo4j" {
				c.Username = "neo4j"
			}
			op := model.ResourceContext{Credentials: c, OperationID: "fixture-approved-op", Purge: true}
			cleanupK3sFixtures(t, m, op)
			spec := model.ResourceSpec{Name: "fixture", Type: kind, Storage: "1Gi"}
			provisionK3sFixture(t, ctx, m, spec, op)
			source := checkK3sResourceIsolation(t, ctx, m, spec.Name, c)
			query := k3sFixtureQuery(t, ctx, m, kind)
			query(source, c, true)
			// Exercise each supported lifecycle transition before checking durable data.
			for _, action := range []string{"stop", "start", "restart"} {
				// Require the requested fixture lifecycle transition to succeed.
				if e = m.Action(ctx, spec.Name, action, op); e != nil {
					t.Fatal(action, e)
				}
			}
			// Fail the fixture on lifecycle lost data.
			if !strings.Contains(query(source, c, false), "durable") {
				t.Fatal("lifecycle lost data")
			}
			checkK3sIndependentRestore(t, ctx, m, spec, op, c, query)
			checkK3sResourcePurge(t, ctx, m, spec.Name, source.Network, op)
		})
	}
}

// k3sFixtureQuery builds native authenticated fixture reads and writes.
func k3sFixtureQuery(t *testing.T, ctx context.Context, m *K3sManager, kind string) func(record, model.ResourceCredentials, bool) string {
	// Run a fixture read or mutation through the selected engine using its supplied password.
	return func(item record, credentials model.ResourceCredentials, write bool) string {
		t.Helper()
		script := ""
		// Select the native behavior for the requested engine or lifecycle operation.
		switch kind {
		// Use PostgreSQL native commands and its authenticated database configuration.
		case "postgres":
			sql := "SELECT value FROM fixture"
			// Choose the fixture mutation instead of its read-only verification query.
			if write {
				sql = "CREATE TABLE fixture(value text); INSERT INTO fixture VALUES ('durable')"
			}
			script = `IFS= read -r PGPASSWORD; export PGPASSWORD; exec psql -h 127.0.0.1 -U hostlier -d app -Atc "` + sql + `"`
		// Use Redis native commands and its authenticated persistence configuration.
		case "redis":
			command := "GET hostlier-fixture"
			// Choose the fixture mutation instead of its read-only verification query.
			if write {
				command = "SET hostlier-fixture durable"
			}
			script = `IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec redis-cli --user hostlier ` + command
		// Use Neo4j native commands and its authenticated database configuration.
		case "neo4j":
			cypher := "MATCH (n:HostlierFixture) RETURN n.value;"
			// Choose the fixture mutation instead of its read-only verification query.
			if write {
				cypher = "CREATE (:HostlierFixture {value: \"durable\"});"
			}
			script = `IFS= read -r NEO4J_PASSWORD; export NEO4J_PASSWORD; exec cypher-shell -a bolt://127.0.0.1:7687 -u neo4j '` + cypher + `'`
		}
		out, e := m.engineExec(ctx, item, script, []byte(credentials.Password+"\n"))
		// Require the authenticated engine command to complete successfully.
		if e != nil {
			t.Fatal(e)
		}
		return string(out)
	}
}

// checkK3sResourceIsolation checks private services and rejects incorrect credentials.
func checkK3sResourceIsolation(t *testing.T, ctx context.Context, m *K3sManager, name string, c model.ResourceCredentials) record {
	t.Helper()
	source, e := m.find(name)
	// Require an existing resource record before operating on engine state.
	if e != nil {
		t.Fatal(e)
	}
	service, e := m.get(ctx, "service", "engine", source.Network)
	// Require the explicitly scoped Kubernetes object lookup to complete.
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(service)
	// Fail the fixture on database exposed outside private service.
	if strings.Contains(string(raw), `"nodePort"`) || !strings.Contains(string(raw), `"type":"ClusterIP"`) {
		t.Fatal("database exposed outside private service")
	}
	wrong := c
	wrong.Password = "incorrect-private-password"
	// Fail the fixture on incorrect credentials authenticated.
	if _, e = m.protocol(ctx, source, wrong); e == nil {
		t.Fatal("incorrect credentials authenticated")
	}

	return source
}

// checkK3sIndependentRestore verifies native backups with independent target credentials.
func checkK3sIndependentRestore(t *testing.T, ctx context.Context, m *K3sManager, spec model.ResourceSpec, op model.ResourceContext, c model.ResourceCredentials, query func(record, model.ResourceCredentials, bool) string) {
	t.Helper()
	backup, e := m.Backup(ctx, spec.Name, op)
	// Fail the fixture on backup.
	if e != nil {
		t.Fatal("backup", e)
	}
	// Fail the fixture on invalid backup or credential leak.
	if len(backup) == 0 || bytes.Contains(backup, []byte(c.Password)) {
		t.Fatal("invalid backup or credential leak")
	}
	targetC := c
	targetC.Password = "restored-private-password-b456"
	targetOp := model.ResourceContext{Credentials: targetC, OperationID: "restore-approved-op", Purge: true}
	targetSpec := spec
	targetSpec.Name = "restored"
	// Fail the fixture on restore target.
	if _, e = m.Provision(ctx, targetSpec, targetOp); e != nil {
		t.Fatal("restore target", e)
	}
	// Fail the fixture on restore.
	if e = m.Restore(ctx, targetSpec.Name, backup, targetOp); e != nil {
		t.Fatal("restore", e)
	}
	target, e := m.find(targetSpec.Name)
	// Require an existing resource record before operating on engine state.
	if e != nil {
		t.Fatal(e)
	}
	// Fail the fixture on independent restore lacks data.
	if !strings.Contains(query(target, targetC, false), "durable") {
		t.Fatal("independent restore lacks data")
	}
	// Require the requested fixture lifecycle transition to succeed.
	if e = m.Action(ctx, targetSpec.Name, "restart", targetOp); e != nil {
		t.Fatal(e)
	}
	// Fail the fixture on restored data lost after restart.
	if !strings.Contains(query(target, targetC, false), "durable") {
		t.Fatal("restored data lost after restart")
	}
}

// checkK3sResourcePurge distinguishes retained claims from explicitly purged namespaces.
func checkK3sResourcePurge(t *testing.T, ctx context.Context, m *K3sManager, name, network string, op model.ResourceContext) {
	t.Helper()
	var e error
	op.Purge = false
	// Require the requested fixture lifecycle transition to succeed.
	if e = m.Action(ctx, name, "delete", op); e != nil {
		t.Fatal(e)
	}
	claim, e := m.get(ctx, "pvc", "data", network)
	// Fail the fixture on ordinary deletion lost PVC.
	if e != nil || claim == nil {
		t.Fatal("ordinary deletion lost PVC", e)
	}
	op.Purge = true
	// Require the requested fixture lifecycle transition to succeed.
	if e = m.Action(ctx, name, "delete", op); e != nil {
		t.Fatal(e)
	}
	namespace, e := m.get(ctx, "namespace", network, "")
	// Fail the fixture on purge retained namespace.
	if e != nil || namespace != nil {
		t.Fatal("purge retained namespace", e)
	}
}

// provisionK3sFixture provisions the source and redacts credentials from failure diagnostics.
func provisionK3sFixture(t *testing.T, ctx context.Context, m *K3sManager, spec model.ResourceSpec, op model.ResourceContext) {
	t.Helper()
	resource, e := m.Provision(ctx, spec, op)
	// Keep failure diagnostics available without masking the original operation result.
	if e != nil {
		// Emit redacted diagnostics only when log retrieval succeeds.
		if logs, logErr := m.Logs(ctx, spec.Name); logErr == nil {
			t.Log(strings.ReplaceAll(logs, op.Credentials.Password, "[REDACTED]"))
		}
		t.Fatal(e)
	}
	t.Log("provisioned", spec.Type, resource.Network)
}

// cleanupK3sFixtures purges only the source and independent restore fixture namespaces.
func cleanupK3sFixtures(t *testing.T, m *K3sManager, op model.ResourceContext) {
	names := []string{"fixture", "restored"}
	// Purge only the source and restore target namespaces after fixture verification.
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Minute)
		defer done()
		// Clean up each fixture namespace without affecting unrelated resources.
		for _, name := range names {
			// Keep failure diagnostics available without masking the original operation result.
			if e := m.Action(cleanup, name, "delete", op); e != nil && !code(e, "RESOURCE_NOT_FOUND") {
				t.Log("cleanup", name, e)
			}
		}
	})
}

// TestLiveK3sResize verifies persisted database contents across allocation-only pod recreation.
func TestLiveK3sResize(t *testing.T) {
	// Infrastructure mutations require explicit disposable-VM opt in.
	if os.Getenv("HOSTLIER_LIVE_K3S_RESOURCES") != "1" {
		t.Skip("live k3s opt in required")
	}
	// Verify the same data-preserving resize contract for each built-in engine.
	for _, kind := range []string{"postgres", "redis", "neo4j"} {
		// Keep engine failures and cleanup isolated in a dedicated subtest.
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			// Keep bounded fixture diagnostics available for failed engine readiness.
			adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir(), HealthTimeout: 2 * time.Minute, OnDiagnostic: func(value string) { t.Log(strings.ReplaceAll(value, "fixture-private-password-a123", "[REDACTED]")) }})
			// Require the selected resource backend to initialize successfully.
			if err != nil {
				t.Fatal(err)
			}
			m := adapter.(*K3sManager)
			c := model.ResourceCredentials{Username: "hostlier", Password: "fixture-private-password-a123", Database: "app"}
			// Neo4j needs its fixed administrator and configured heap headroom.
			if kind == "neo4j" {
				c.Username = "neo4j"
			}
			op := model.ResourceContext{Credentials: c, OperationID: "resize-approved", Purge: true}
			spec := model.ResourceSpec{Name: "resize-fixture", Type: kind, Storage: "1Gi"}
			// Provision and authenticate this test-owned engine before maintenance.
			if _, err = m.Provision(ctx, spec, op); err != nil {
				t.Fatal(err)
			}
			defer m.Action(context.Background(), spec.Name, "delete", op)
			prior, err := m.find(spec.Name)
			// Require successful resource observation before advancing maintenance.
			if err != nil {
				t.Fatal(err)
			}
			query := k3sFixtureQuery(t, ctx, m, kind)
			query(prior, c, true)
			next := prior.Spec
			next.CPU = "0.5"
			next.Memory = "512Mi"
			// Neo4j needs its fixed administrator and configured heap headroom.
			if kind == "neo4j" {
				next.Memory = "1536Mi"
			}
			// Apply the validated allocation and require authenticated readiness.
			if _, err = m.Resize(ctx, next, op); err != nil {
				t.Fatal(err)
			}
			checkK3sResizedData(t, ctx, m, prior, next, c, query)
			checkK3sFailedResize(t, ctx, m, next, op, query)
		})
	}
}

// checkK3sResizedData compares desired and observed limits while reading pre-maintenance contents.
func checkK3sResizedData(t *testing.T, ctx context.Context, m *K3sManager, prior record, next model.ResourceSpec, c model.ResourceCredentials, query func(record, model.ResourceCredentials, bool) string) {
	t.Helper()

	retained, err := m.find(next.Name)
	// Require successful resource observation before advancing maintenance.
	if err != nil {
		t.Fatal(err)
	}
	// Require desired limits and persistent volume identity to remain intact.
	if retained.Spec != next || retained.Volume != prior.Volume {
		t.Fatal("desired allocation or data identity changed")
	}
	// Read the pre-maintenance record using the unchanged database credentials.
	if !strings.Contains(query(retained, c, false), "durable") {
		t.Fatal("resize lost database content")
	}
	deployment, err := m.get(ctx, "deployment", "engine", prior.Network)
	// Require successful resource observation before advancing maintenance.
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := engineAllocation(deployment)
	// Require successful resource observation before advancing maintenance.
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(allocation)
	// Compare observed runtime enforcement with the exact approved allocation.
	if !strings.Contains(string(encoded), `"cpu":"500m"`) || !strings.Contains(string(encoded), next.Memory) {
		t.Fatalf("unexpected runtime allocation %s", encoded)
	}
}

// checkK3sFailedResize injects a lost patch acknowledgement after a real cluster allocation update.
func checkK3sFailedResize(t *testing.T, ctx context.Context, m *K3sManager, prior model.ResourceSpec, op model.ResourceContext, query func(record, model.ResourceCredentials, bool) string) {
	t.Helper()
	failed := false
	var intercept func(context.Context, []string, []byte) ([]byte, error)
	// Forward real cluster operations while injecting one lost patch acknowledgement.
	intercept = func(callCtx context.Context, args []string, input []byte) ([]byte, error) {
		m.options.KubeRun = nil
		output, err := m.run(callCtx, args[4:], input)
		m.options.KubeRun = intercept
		// A successful real patch can lose its acknowledgement before readiness is checked.
		if !failed && len(args) > 4 && args[4] == "patch" && err == nil {
			failed = true
			return output, model.Err("RESOURCE_KUBERNETES", "injected lost allocation acknowledgement")
		}
		return output, err
	}
	m.options.KubeRun = intercept
	next := prior
	next.CPU = "0.75"
	_, err := m.Resize(ctx, next, op)
	m.options.KubeRun = nil
	// The operation must fail after its actual patch and successfully restore the prior allocation.
	if err == nil || !failed {
		t.Fatal("allocation failure injection did not execute", err)
	}
	item, err := m.find(prior.Name)
	// Runtime rollback must not commit the rejected desired allocation.
	if err != nil || item.Spec != prior {
		t.Fatal("failed resize changed desired allocation", err)
	}
	checkK3sResizedData(t, ctx, m, item, prior, op.Credentials, query)
}
