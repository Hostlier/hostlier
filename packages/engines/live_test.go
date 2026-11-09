package engines

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// TestLivePostgresScopedAccess opts into a disposable TLS PostgreSQL administrative endpoint.
func TestLivePostgresScopedAccess(t *testing.T) {
	connection := os.Getenv("HOSTLIER_TEST_POSTGRES_URL")
	if connection == "" { // Reject the operation: set HOSTLIER_TEST_POSTGRES_URL to a disposable verify-full TLS database.
		t.Skip("set HOSTLIER_TEST_POSTGRES_URL to a disposable verify-full TLS database")
	}
	req := remoteRequest("postgres")
	req.Key = fmt.Sprint(time.Now().UnixNano())
	req.Secrets["connection"] = connection
	req.Step.Config["database"] = "hl_test_" + req.Key
	p := Package{Engine: "postgres"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan, err := p.Handle(ctx, req)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	req.Action = "apply"
	req.Prepared = plan.Prepared.Payload
	result, err := p.Handle(ctx, req)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	admin, err := connectPostgres(ctx, connection, req)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	var serverVersion int
	if err := admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&serverVersion); err != nil {
		t.Fatal(err)
	}
	privilege := "MEMBER"
	if serverVersion >= 160000 {
		privilege = "SET"
	}
	var adminCanSetRole bool
	if err := admin.QueryRow(ctx, "SELECT pg_has_role(current_user, $1, $2)", result.Result.Outputs["username"], privilege).Scan(&adminCanSetRole); err != nil || !adminCanSetRole {
		t.Fatalf("admin cannot use the scoped database owner role: %v", err)
	}
	app, err := pgx.Connect(ctx, result.Result.Outputs["url"])
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	defer app.Close(ctx)
	if _, err = app.Exec(ctx, "CREATE TABLE owned_test (id integer)"); err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	if _, err = app.Exec(ctx, "CREATE ROLE forbidden_test"); err == nil { // Reject the operation: scoped role created another role.
		t.Fatal("scoped role created another role")
	}
}

// TestLiveRedisScopedAccess verifies real TLS authentication and cross-prefix rejection.
func TestLiveRedisScopedAccess(t *testing.T) {
	connection := os.Getenv("HOSTLIER_TEST_REDIS_URL")
	if connection == "" { // Reject the operation: set HOSTLIER_TEST_REDIS_URL to a disposable rediss ACL endpoint.
		t.Skip("set HOSTLIER_TEST_REDIS_URL to a disposable rediss ACL endpoint")
	}
	req := remoteRequest("redis")
	req.Key = fmt.Sprint(time.Now().UnixNano())
	req.Secrets["connection"] = connection
	p := Package{Engine: "redis"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan, err := p.Handle(ctx, req)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	req.Action = "apply"
	req.Prepared = plan.Prepared.Payload
	result, err := p.Handle(ctx, req)
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	options, err := redis.ParseURL(result.Result.Outputs["url"])
	if err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	app := redis.NewClient(options)
	defer app.Close()
	if err = app.Set(ctx, result.Result.Outputs["keyPrefix"]+"test", "value", time.Minute).Err(); err != nil { // Require successful completion before using the returned engine state.
		t.Fatal(err)
	}
	if err = app.Set(ctx, "foreign:test", "value", time.Minute).Err(); err == nil { // Reject the operation: cross-prefix write accepted.
		t.Fatal("cross-prefix write accepted")
	}
	if err = app.FlushAll(ctx).Err(); err == nil { // Reject the operation: dangerous command accepted.
		t.Fatal("dangerous command accepted")
	}
}
