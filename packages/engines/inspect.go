package engines

import (
	"context"
	"encoding/json"
	"hostlier/pkg/workflow"
)

// inspect never repairs or replays uncertain effects; only independently verified results escape.
func (p Package) inspect(ctx context.Context, req workflow.Request, c Config, approved payload) (workflow.Inspection, error) {
	// Native managers prove runtime ownership and authenticate the exact approved credentials.
	if c.Mode == "new-instance" {
		return p.inspectLocal(ctx, req, approved)
	}
	// Remote protocols require independent catalog or ACL ownership verification.
	if p.Engine == "postgres" {
		return inspectPostgres(ctx, req, c, approved)
	}
	return inspectRedis(ctx, req, c, approved)
}

// inspectLocal is deliberately uncertain for missing metadata because storage may already exist.
func (p Package) inspectLocal(ctx context.Context, req workflow.Request, approved payload) (workflow.Inspection, error) {
	m, err := manager(req)
	// Unavailable runtime evidence cannot prove absence or success.
	if err != nil {
		return uncertain(), nil
	}
	verifier, ok := m.(nativeVerifier)
	// Require authenticated verification, never synthesize success from metadata alone.
	if !ok || verifier.VerifyWorkflow(ctx, localName(req), approved.Credentials) != nil {
		return uncertain(), nil
	}
	item, err := m.Status(ctx, localName(req))
	// A vanished owned object invalidates the observation.
	if err != nil || item.Type != p.Engine {
		return uncertain(), nil
	}
	outputs, err := m.Connection(ctx, item.Name, approved.Credentials)
	// Publish only connection fields belonging to the verified instance.
	if err != nil {
		return uncertain(), nil
	}
	enrich(outputs, approved.Credentials)
	// Redis does not expose a separate SQL database.
	if p.Engine == "redis" {
		delete(outputs, "database")
	}
	state, _ := json.Marshal(approved)
	return completed(workflow.Result{State: state, Outputs: outputs, Networks: []string{item.Network}}), nil
}

// uncertain deliberately contains no outputs that a dependent application could consume.
func uncertain() workflow.Inspection { return workflow.Inspection{Status: workflow.Uncertain} }

// completed publishes only a result verified through authenticated read-only discovery.
func completed(result workflow.Result) workflow.Inspection {
	return workflow.Inspection{Status: workflow.Completed, Result: &result}
}
