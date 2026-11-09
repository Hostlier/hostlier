package engines

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	appURL := scopedPostgresURL(u, c, p)
	var app *pgx.Conn
	if len(req.Previous) == 0 {
		app, err = connectFreshPostgres(ctx, appURL.String(), req)
	} else {
		app, err = connectPostgres(ctx, appURL.String(), req)
		if err == nil {
			err = verifyPostgresApp(ctx, app)
		}
	}
	if err != nil {
		if app != nil {
			app.Close(ctx)
		}
		return workflow.Result{}, errors.New("postgres scoped connection or verification failed")
	}
	defer app.Close(ctx)
	p.Credentials.Database = c.Database
	state, _ := json.Marshal(p)
	outputs := map[string]string{"url": appURL.String()}
	enrich(outputs, p.Credentials)
	exportTrust(outputs, req)
	return workflow.Result{State: state, Outputs: outputs}, nil
}

// verifyPostgresApp requires the authenticated role, database ACL and health
// to be ready before any scoped credential is published.
func verifyPostgresApp(ctx context.Context, app *pgx.Conn) error {
	if err := verifyPostgresScope(ctx, app); err != nil {
		return err
	}
	if err := verifyPostgresIsolation(ctx, app); err != nil {
		return err
	}
	if err := app.Ping(ctx); err != nil {
		return errors.New("postgres scoped health failed")
	}
	return nil
}

// scopedPostgresURL replaces only the administrative identity and database path.
// Verified TLS and required channel binding remain on the scoped application URL.
func scopedPostgresURL(admin *url.URL, c Config, p payload) *url.URL {
	scoped := *admin
	scoped.User = url.UserPassword(p.Credentials.Username, p.Credentials.Password)
	scoped.Path = "/" + c.Database
	return &scoped
}

type postgresExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// createPostgres quotes identifiers and credentials without interpolating provider data into SQL syntax.
func createPostgres(ctx context.Context, db postgresExecutor, c Config, p payload, serverVersion int) error {
	role := pgx.Identifier{p.Credentials.Username}.Sanitize()
	database := pgx.Identifier{c.Database}.Sanitize()
	// Generated passwords contain only hexadecimal characters and cannot terminate a SQL literal.
	if _, err := db.Exec(ctx, "CREATE ROLE "+role+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '"+p.Credentials.Password+"'"); err != nil {
		return errors.New("postgres role creation failed")
	}
	// PostgreSQL 16+ requires SET membership to create a database owned by another role.
	// Older releases use plain membership for the same ownership check.
	grant := "GRANT " + role + " TO CURRENT_USER"
	if serverVersion >= 160000 {
		grant += " WITH SET TRUE, INHERIT FALSE"
	}
	// The administrative role gains access to the new role; the application role
	// gains no membership in the administrative role.
	if _, err := db.Exec(ctx, grant); err != nil {
		return rollbackCreatedPostgresRole(db, role, "postgres role ownership grant failed")
	}
	if _, err := db.Exec(ctx, "CREATE DATABASE "+database+" OWNER "+role); err != nil {
		return rollbackCreatedPostgresRole(db, role, "postgres database creation failed")
	}
	// The new role owns the database. Run the revocation as that owner so
	// PostgreSQL removes the default PUBLIC privileges instead of warning that
	// the administrative role has no grant options.
	if _, err := db.Exec(ctx, "SET ROLE "+role); err != nil {
		return errors.New("postgres database isolation failed; inspect uncertain operation")
	}
	_, revokeErr := db.Exec(ctx, "REVOKE ALL ON DATABASE "+database+" FROM PUBLIC")
	resetCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, resetErr := db.Exec(resetCtx, "RESET ROLE")
	cancel()
	if revokeErr != nil || resetErr != nil {
		return errors.New("postgres database isolation failed; inspect uncertain operation")
	}
	return nil
}

// rollbackCreatedPostgresRole removes only the role created by this attempt. DROP ROLE
// fails if the database was created despite a lost response, leaving it for inspection.
func rollbackCreatedPostgresRole(db postgresExecutor, role, cause string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.Exec(ctx, "DROP ROLE "+role); err != nil {
		return errors.New(cause + "; inspect uncertain operation")
	}
	return errors.New(cause)
}

// verifyPostgresScope refuses changed server privileges or database ownership before publication.
func verifyPostgresScope(ctx context.Context, app *pgx.Conn) error {
	var safe bool
	err := app.QueryRow(ctx, `SELECT NOT r.rolsuper AND NOT r.rolcreatedb AND NOT r.rolcreaterole AND NOT r.rolreplication AND NOT r.rolbypassrls AND d.datdba=r.oid AND NOT EXISTS (SELECT FROM pg_auth_members WHERE member=r.oid) FROM pg_roles r JOIN pg_database d ON d.datname=current_database() WHERE r.rolname=current_user`).Scan(&safe)
	if err != nil || !safe {
		return errors.New("postgres scoped privileges or ownership verification failed")
	}
	return nil
}

// verifyPostgresIsolation checks the actual database ACL because REVOKE can
// complete with a warning when the caller has no grant options.
func verifyPostgresIsolation(ctx context.Context, app *pgx.Conn) error {
	var isolated bool
	err := app.QueryRow(ctx, `SELECT NOT EXISTS (SELECT FROM pg_database d, aclexplode(COALESCE(d.datacl, acldefault('d', d.datdba))) a WHERE d.datname=current_database() AND a.grantee=0)`).Scan(&isolated)
	if err != nil || !isolated {
		return errors.New("postgres database isolation verification failed")
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
	var serverVersion int
	if err := db.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&serverVersion); err != nil {
		return errors.New("postgres server version lookup failed")
	}
	return createPostgres(ctx, db, c, p, serverVersion)
}

// connectPostgres installs a protected provider CA before any socket is opened.
func connectPostgres(ctx context.Context, uri string, req workflow.Request) (*pgx.Conn, error) {
	config, err := postgresConnectionConfig(uri, req)
	if err != nil {
		return nil, err
	}
	return pgx.ConnectConfig(ctx, config)
}

// connectFreshPostgres allows a just-created role and database to become
// available through provider connection routing without repeating any mutation.
func connectFreshPostgres(ctx context.Context, uri string, req workflow.Request) (*pgx.Conn, error) {
	config, err := postgresConnectionConfig(uri, req)
	if err != nil {
		return nil, err
	}
	return retryPostgresConnect(ctx, config, func(attemptCtx context.Context, config *pgx.ConnConfig) (*pgx.Conn, error) {
		app, err := pgx.ConnectConfig(attemptCtx, config)
		if err != nil {
			return nil, err
		}
		if err = verifyPostgresApp(attemptCtx, app); err != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			app.Close(closeCtx)
			cancel()
			return nil, err
		}
		return app, nil
	}, []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second,
		2 * time.Second, 4 * time.Second, 4 * time.Second,
	})
}

// retryPostgresConnect retries only the scoped connection, with the same
// verified TLS and channel-binding configuration on every bounded attempt.
func retryPostgresConnect(ctx context.Context, config *pgx.ConnConfig, dial func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error), delays []time.Duration) (*pgx.Conn, error) {
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := dial(attemptCtx, config)
		cancel()
		if err == nil {
			return conn, nil
		}
		if postgresCertificateFailure(err) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == len(delays) {
			return nil, err
		}
		timer := time.NewTimer(delays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func postgresCertificateFailure(err error) bool {
	var verification *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verification) || errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &invalid)
}

// postgresConnectionConfig installs a protected provider CA before any socket is opened.
func postgresConnectionConfig(uri string, req workflow.Request) (*pgx.ConnConfig, error) {
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
	return config, nil
}
