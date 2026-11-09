package engines

import (
	"context"
	"encoding/json"
	"hostlier/pkg/workflow"
	"net/url"
)

// inspectPostgres proves absence or verifies generated role authentication, ownership and isolation.
func inspectPostgres(ctx context.Context, req workflow.Request, c Config, p payload) (workflow.Inspection, error) {
	u, err := connection("postgres", req)
	// Invalid transport cannot establish an authoritative outcome.
	if err != nil {
		return uncertain(), nil
	}
	admin, err := connectPostgres(ctx, u.String(), req)
	// Administrative catalog discovery is required even if scoped login would succeed.
	if err != nil {
		return uncertain(), nil
	}
	defer admin.Close(ctx)
	var role, database bool
	err = admin.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_roles WHERE rolname=$1), EXISTS (SELECT FROM pg_database WHERE datname=$2)", p.Credentials.Username, c.Database).Scan(&role, &database)
	// Query errors never establish absence.
	if err != nil {
		return uncertain(), nil
	}
	// Neither object exists, so this package has not created any owned effects.
	if !role && !database {
		return workflow.Inspection{Status: workflow.NotStarted}, nil
	}
	// Partial provisioning requires investigation instead of mutation replay.
	if !role || !database {
		return uncertain(), nil
	}
	return verifyPostgresResult(ctx, req, c, p, u)
}

// verifyPostgresResult authenticates approved credentials and checks the full resulting SQL scope.
func verifyPostgresResult(ctx context.Context, req workflow.Request, c Config, p payload, u *url.URL) (workflow.Inspection, error) {
	u.User = url.UserPassword(p.Credentials.Username, p.Credentials.Password)
	u.Path = "/" + c.Database
	app, err := connectPostgres(ctx, u.String(), req)
	// Exact generated credentials bind the deterministic role to the approved request.
	if err != nil {
		return uncertain(), nil
	}
	defer app.Close(ctx)
	// Ownership and restricted privileges must still match the approved outcome.
	if verifyPostgresScope(ctx, app) != nil {
		return uncertain(), nil
	}
	var isolated bool
	err = app.QueryRow(ctx, `SELECT NOT EXISTS (SELECT FROM pg_database d, aclexplode(COALESCE(d.datacl, acldefault('d', d.datdba))) a WHERE d.datname=current_database() AND a.grantee=0)`).Scan(&isolated)
	// A crash before PUBLIC revocation must never report completion.
	if err != nil || !isolated {
		return uncertain(), nil
	}
	p.Credentials.Database = c.Database
	state, _ := json.Marshal(p)
	outputs := map[string]string{"url": u.String()}
	enrich(outputs, p.Credentials)
	exportTrust(outputs, req)
	return completed(workflow.Result{State: state, Outputs: outputs}), nil
}
