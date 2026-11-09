package resources

import (
	"context"
	"hostlier/internal/model"
	"hostlier/internal/recovery"
	"os"
	"testing"
	"time"
)

// TestLiveRecoveryTransitions exercises native data, independent storage and failure recovery on an explicitly selected local Docker runtime.
func TestLiveRecoveryTransitions(t *testing.T) {
	// Fail the fixture if the recovery invariant is violated.
	if os.Getenv("HOSTLIER_RECOVERY_DOCKER") != "1" {
		t.Skip("set HOSTLIER_RECOVERY_DOCKER=1 for isolated native-engine tests")
	}
	objects := liveS3(t)
	// Exercise each native recovery case independently.
	for _, engine := range []string{"postgres", "redis", "neo4j"} {
		// Keep the callback bound to this fixture and its cleanup lifetime.
		t.Run(engine, func(t *testing.T) { liveRecoveryTransition(t, engine, objects) })
	}
}

// liveRecoveryTransition checks both a failed restore and a verified upgrade without reusing source data volumes.
func liveRecoveryTransition(t *testing.T, engine string, objects *recovery.S3) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	// Keep the callback bound to this fixture and its cleanup lifetime.
	adapter, err := New(Options{Type: "docker", StateDir: t.TempDir(), SocketPath: os.Getenv("HOSTLIER_DOCKER_SOCKET"), OnDiagnostic: func(value string) { t.Log(value) }})
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal(err)
	}
	m := adapter.(*Manager)
	source := model.ResourceSpec{Name: "source", Type: engine, Storage: "1Gi"}
	target := source
	target.Name = "target"
	version := map[string]string{"postgres": "17", "redis": "8.2", "neo4j": "2025.01"}[engine]
	target.Image = model.UpgradeImage(engine, version)
	credentials := liveRecoveryCredentials(engine)
	op := model.ResourceContext{OperationID: "fixture-recovery", Credentials: credentials, Purge: true}
	// Keep the callback bound to this fixture and its cleanup lifetime.
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		m.Action(cleanup, "target", "delete", op)
		m.Action(cleanup, "source", "delete", op)
	})
	before, data := liveRecoverySource(t, m, ctx, source, target, op)
	// Stop the fixture on an unexpected operation result.
	if err = m.Action(ctx, "source", "stop", op); err != nil {
		t.Fatal(err)
	}
	// Stop the fixture on an unexpected operation result.
	if err = m.Restore(ctx, "target", []byte("corrupt-native-snapshot"), op); err == nil {
		t.Fatal("corrupt native snapshot accepted")
	}
	data = liveSnapshotRoundTrip(t, ctx, objects, source, before, data)
	// Stop the fixture on an unexpected operation result.
	if err = m.Restore(ctx, "target", data, op); err != nil {
		t.Fatal("valid restore after rejected corruption", err)
	}
	after, err := m.Evidence(ctx, "target", op)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal("restored evidence", err)
	}
	// Stop the fixture on an unexpected operation result.
	if err = recovery.Match(before, after, time.Now().UnixMilli()); err != nil {
		t.Fatalf("content mismatch: %v source=%+v target=%+v", err, before, after)
	}
	// Stop the fixture on an unexpected operation result.
	if err = m.Fence(ctx, "target", false, op); err != nil {
		t.Fatal("cutover", err)
	}
	status, err := m.Status(ctx, "source")
	// Stop the fixture on an unexpected operation result.
	if err != nil || status.Status != "stopped" {
		t.Fatal("source revived", status, err)
	}
	t.Logf("verified %s -> %s; snapshot %d bytes; native data matched", before.Version, after.Version, len(data))
}

// liveSeed writes real engine-native data that readiness-only tests could not recover.
func liveSeed(t *testing.T, m *Manager, ctx context.Context, engine string, c model.ResourceCredentials) {
	t.Helper()
	commands := map[string][]string{
		"postgres": {"psql", "-h", "127.0.0.1", "-U", c.Username, "-d", c.Database, "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE recovery_fixture(id serial primary key, value text not null); INSERT INTO recovery_fixture(value) VALUES ('preserve me'),('unicode λ');"},
		"redis":    {"redis-cli", "--user", c.Username, "EVAL", "redis.call('SET','durable','preserve me'); redis.call('HSET','details','field','unicode'); redis.call('SET','ttl','expires','PX',3600000); redis.call('RPUSH','list','first','second'); redis.call('SADD','set','b','a'); redis.call('ZADD','scores',1,'a',2,'b'); redis.call('SELECT',15); redis.call('SET','other-db','preserve db15'); return 1", "0"},
		"neo4j":    {"cypher-shell", "-a", "bolt://127.0.0.1:7687", "-u", c.Username, "CREATE (a:Recovery {name:'preserve me'}), (b:Recovery {name:'other'}), (a)-[:KNOWS {weight:3}]->(b);"},
	}
	// Stop the fixture on an unexpected operation result.
	if _, err := m.execute(ctx, "source", commands[engine], authEnv(engine, c)); err != nil {
		t.Fatal("seed data", err)
	}
}

// liveRecoveryCredentials supplies fixture-only native authentication without recording any user secret.
func liveRecoveryCredentials(engine string) model.ResourceCredentials {
	c := model.ResourceCredentials{Username: "hostlier", Password: "hostlier-integration-private-password", Database: "app"}
	// Fail the fixture if the recovery invariant is violated.
	if engine == "neo4j" {
		c.Username = "neo4j"
	}
	return c
}

// liveRecoverySource proves the native version transition and captures a fenced source snapshot.
func liveRecoverySource(t *testing.T, m *Manager, ctx context.Context, source, target model.ResourceSpec, op model.ResourceContext) (model.DatabaseEvidence, []byte) {
	t.Helper()
	var err error
	// Stop the fixture on an unexpected operation result.
	if _, err = m.Provision(ctx, source, op); err != nil {
		t.Fatal("source provision", err)
	}
	// Stop the fixture on an unexpected operation result.
	if _, err = m.Provision(ctx, target, op); err != nil {
		t.Fatal("target provision", err)
	}
	liveSeed(t, m, ctx, source.Type, op.Credentials)
	before, err := m.Evidence(ctx, "source", op)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal("source evidence", err)
	}
	after, err := m.Evidence(ctx, "target", op)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal("target evidence", err)
	}
	// Fail the fixture if the recovery invariant is violated.
	if recovery.Match(before, after, time.Now().UnixMilli()) == nil {
		t.Fatal("empty target incorrectly matched seeded source")
	}
	// Fail the fixture if the recovery invariant is violated.
	if !model.SupportedTransition(source.Type, before.Version, after.Version) {
		t.Fatalf("unsupported actual versions %s -> %s", before.Version, after.Version)
	}
	// Stop the fixture on an unexpected operation result.
	if err = m.Fence(ctx, "source", true, op); err != nil {
		t.Fatal("source fence", err)
	}
	// Stop the fixture on an unexpected operation result.
	if err = m.Fence(ctx, "target", true, op); err != nil {
		t.Fatal("target fence", err)
	}
	data, err := m.Backup(ctx, "source", op)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal("native backup", err)
	}
	return before, data
}
