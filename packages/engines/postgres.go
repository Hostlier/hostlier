package engines

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/jackc/pgx/v5"
	"hostlier/pkg/workflow"
)

// postgresApply creates a database and a nonsuperuser role, refusing unknown existing names.
func postgresApply(ctx context.Context, req workflow.Request, c Config, p payload) (workflow.Result, error) {
	u, err := connection("postgres", req)
	if err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	db, err := connectPostgres(ctx, u.String(), req)
	if err != nil { // Reject the operation: postgres administrative connection failed.
		return workflow.Result{}, errors.New("postgres administrative connection failed")
	}
	defer db.Close(ctx)
	if err = ensurePostgres(ctx, db, req, c, p); err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	u.User = url.UserPassword(p.Credentials.Username, p.Credentials.Password)
	u.Path = "/" + c.Database
	app, err := connectPostgres(ctx, u.String(), req)
	if err != nil { // Reject the operation: postgres scoped authentication failed.
		return workflow.Result{}, errors.New("postgres scoped authentication failed")
	}
	defer app.Close(ctx)
	if err = verifyPostgresScope(ctx, app); err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	if err = app.Ping(ctx); err != nil { // Reject the operation: postgres scoped health failed.
		return workflow.Result{}, errors.New("postgres scoped health failed")
	}
	p.Credentials.Database = c.Database
	state, _ := json.Marshal(p)
	outputs := map[string]string{"url": u.String()}
	enrich(outputs, p.Credentials)
	exportTrust(outputs, req)
	return workflow.Result{State: state, Outputs: outputs}, nil
}

// createPostgres quotes identifiers and credentials without interpolating provider data into SQL syntax.
func createPostgres(ctx context.Context, db *pgx.Conn, c Config, p payload) error {
	role := pgx.Identifier{p.Credentials.Username}.Sanitize()
	database := pgx.Identifier{c.Database}.Sanitize()
	// Generated passwords contain only hexadecimal characters and cannot terminate a SQL literal.
	if _, err := db.Exec(ctx, "CREATE ROLE "+role+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '"+p.Credentials.Password+"'"); err != nil {
		return errors.New("postgres role creation failed")
	}
	if _, err := db.Exec(ctx, "CREATE DATABASE "+database+" OWNER "+role); err != nil { // Reject the operation: postgres database creation failed; inspect uncertain operation.
		return errors.New("postgres database creation failed; inspect uncertain operation")
	}
	if _, err := db.Exec(ctx, "REVOKE ALL ON DATABASE "+database+" FROM PUBLIC"); err != nil { // Reject the operation: postgres database isolation failed.
		return errors.New("postgres database isolation failed")
	}
	return nil
}

// verifyPostgresScope refuses changed server privileges or database ownership before publication.
func verifyPostgresScope(ctx context.Context, app *pgx.Conn) error {
	var safe bool
	err := app.QueryRow(ctx, `SELECT NOT r.rolsuper AND NOT r.rolcreatedb AND NOT r.rolcreaterole AND NOT r.rolreplication AND NOT r.rolbypassrls AND d.datdba=r.oid AND NOT EXISTS (SELECT FROM pg_auth_members WHERE member=r.oid) FROM pg_roles r JOIN pg_database d ON d.datname=current_database() WHERE r.rolname=current_user`).Scan(&safe)
	if err != nil || !safe { // Reject the operation: postgres scoped privileges or ownership verification failed.
		return errors.New("postgres scoped privileges or ownership verification failed")
	}
	return nil
}

// ensurePostgres checks transport and tracked ownership before any schema mutation.
func ensurePostgres(ctx context.Context, db *pgx.Conn, req workflow.Request, c Config, p payload) error {
	if db.Config().TLSConfig == nil || db.Config().TLSConfig.InsecureSkipVerify || len(db.Config().Fallbacks) > 0 { // Reject the operation: postgres requires exclusively verified TLS.
		return errors.New("postgres requires exclusively verified TLS")
	}
	var exists bool
	if err := db.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_roles WHERE rolname=$1) OR EXISTS (SELECT FROM pg_database WHERE datname=$2)", p.Credentials.Username, c.Database).Scan(&exists); err != nil { // Reject the operation: postgres ownership lookup failed.
		return errors.New("postgres ownership lookup failed")
	}
	if exists && len(req.Previous) == 0 { // Reject the operation: refusing existing untracked postgres role or database.
		return errors.New("refusing existing untracked postgres role or database")
	}
	if exists { // Reuse only a resource already tracked by this dependency.
		return nil
	}
	return createPostgres(ctx, db, c, p)
}

// connectPostgres installs a protected provider CA before any socket is opened.
func connectPostgres(ctx context.Context, uri string, req workflow.Request) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(uri)
	if err != nil { // Reject the operation: invalid postgres connection.
		return nil, errors.New("invalid postgres connection")
	}
	if len(config.Fallbacks) > 0 { // Reject the operation: postgres TLS fallbacks forbidden.
		return nil, errors.New("postgres TLS fallbacks forbidden")
	}
	config.TLSConfig, err = trustedTLS(config.TLSConfig, req)
	if err != nil { // Require successful completion before using the returned engine state.
		return nil, err
	}
	return pgx.ConnectConfig(ctx, config)
}
