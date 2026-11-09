package engines

import (
	"context"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"hostlier/internal/model"
)

type recordingPostgresExecutor struct {
	statements []string
	failAt     map[int]error
}

func TestRetryPostgresConnectKeepsVerifiedConfig(t *testing.T) {
	req := remoteRequest("postgres")
	req.Secrets["connection"] += "&channel_binding=require"
	config, err := postgresConnectionConfig(req.Secrets["connection"], req)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	_, err = retryPostgresConnect(context.Background(), config, func(_ context.Context, candidate *pgx.ConnConfig) (*pgx.Conn, error) {
		attempts++
		if candidate.ChannelBinding != "require" || candidate.TLSConfig == nil || candidate.TLSConfig.InsecureSkipVerify || len(candidate.Fallbacks) != 0 {
			t.Fatal("retry weakened the scoped connection")
		}
		if attempts < 3 {
			return nil, &pgconn.PgError{Code: "28P01"}
		}
		return nil, nil
	}, []time.Duration{0, 0})
	if err != nil || attempts != 3 {
		t.Fatalf("fresh role was not retried: %d %v", attempts, err)
	}
}

func TestRetryPostgresConnectStopsOnCertificateFailureAndCancellation(t *testing.T) {
	config, err := pgx.ParseConfig("postgresql://user:secret@example.com/db?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	_, err = retryPostgresConnect(context.Background(), config, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
		attempts++
		return nil, x509.UnknownAuthorityError{}
	}, []time.Duration{0, 0})
	if err == nil || attempts != 1 {
		t.Fatalf("certificate failure was retried: %d %v", attempts, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	attempts = 0
	_, err = retryPostgresConnect(ctx, config, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
		attempts++
		cancel()
		return nil, errors.New("temporary connection failure")
	}, []time.Duration{0, 0})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("canceled retry continued: %d %v", attempts, err)
	}
}

func TestRetryPostgresConnectRequiresVerificationBeforeSuccess(t *testing.T) {
	config, err := pgx.ParseConfig("postgresql://user:secret@example.com/db?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	_, err = retryPostgresConnect(context.Background(), config, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("temporary query failure")
		}
		return nil, nil
	}, []time.Duration{0})
	if err != nil || attempts != 2 {
		t.Fatalf("transient verification was not retried: %d %v", attempts, err)
	}
	attempts = 0
	_, err = retryPostgresConnect(context.Background(), config, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
		attempts++
		return nil, errors.New("unsafe ACL")
	}, []time.Duration{0, 0})
	if attempts != 3 || err == nil {
		t.Fatalf("unsafe ACL was accepted: %d %v", attempts, err)
	}
}

func (e *recordingPostgresExecutor) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	e.statements = append(e.statements, sql)
	return pgconn.CommandTag{}, e.failAt[len(e.statements)]
}

func TestCreatePostgresGrantsAdminSetBeforeDatabase(t *testing.T) {
	config := Config{Database: "application"}
	prepared := payload{Credentials: model.ResourceCredentials{Username: "hl_scope", Password: "0123456789abcdef"}}
	for _, version := range []struct {
		number int
		grant  string
	}{
		{number: 150000, grant: `GRANT "hl_scope" TO CURRENT_USER`},
		{number: 180000, grant: `GRANT "hl_scope" TO CURRENT_USER WITH SET TRUE, INHERIT FALSE`},
	} {
		db := &recordingPostgresExecutor{}
		if err := createPostgres(context.Background(), db, config, prepared, version.number); err != nil {
			t.Fatal(err)
		}
		if len(db.statements) != 6 ||
			!strings.HasPrefix(db.statements[0], `CREATE ROLE "hl_scope" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION`) ||
			db.statements[1] != version.grant ||
			db.statements[2] != `CREATE DATABASE "application" OWNER "hl_scope"` ||
			db.statements[3] != `SET ROLE "hl_scope"` ||
			db.statements[4] != `REVOKE ALL ON DATABASE "application" FROM PUBLIC` ||
			db.statements[5] != `RESET ROLE` {
			t.Fatalf("unsafe postgres creation sequence for %d: %q", version.number, db.statements)
		}
	}
}

func TestCreatePostgresRollsBackOnlyFreshRole(t *testing.T) {
	config := Config{Database: "application"}
	prepared := payload{Credentials: model.ResourceCredentials{Username: "hl_scope", Password: "0123456789abcdef"}}
	for _, test := range []struct {
		name          string
		failAt        map[int]error
		count         int
		wantUncertain bool
	}{
		{name: "create role", failAt: map[int]error{1: errors.New("failed")}, count: 1},
		{name: "grant", failAt: map[int]error{2: errors.New("failed")}, count: 3},
		{name: "create database", failAt: map[int]error{3: errors.New("failed")}, count: 4},
		{name: "rollback failure", failAt: map[int]error{3: errors.New("failed"), 4: errors.New("failed")}, count: 4, wantUncertain: true},
		{name: "set owner role", failAt: map[int]error{4: errors.New("failed")}, count: 4, wantUncertain: true},
		{name: "revoke", failAt: map[int]error{5: errors.New("failed")}, count: 6, wantUncertain: true},
		{name: "reset role", failAt: map[int]error{6: errors.New("failed")}, count: 6, wantUncertain: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := &recordingPostgresExecutor{failAt: test.failAt}
			err := createPostgres(context.Background(), db, config, prepared, 180000)
			if err == nil || len(db.statements) != test.count || strings.Contains(err.Error(), "uncertain") != test.wantUncertain {
				t.Fatalf("unexpected failure handling: %v, statements %q", err, db.statements)
			}
			if test.name == "grant" || test.name == "create database" || test.name == "rollback failure" {
				if db.statements[len(db.statements)-1] != `DROP ROLE "hl_scope"` {
					t.Fatalf("fresh role was not rolled back: %q", db.statements)
				}
			}
			if test.name == "revoke" && db.statements[len(db.statements)-1] != "RESET ROLE" {
				t.Fatalf("administrative role was not restored after revocation failure: %q", db.statements)
			}
		})
	}
}
