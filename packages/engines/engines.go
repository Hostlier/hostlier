// Package engines implements optional native database workflows.
package engines

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"hostlier/internal/model"
	"hostlier/internal/resources"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/workflow"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
var prefix = regexp.MustCompile(`^[a-zA-Z0-9:_-]{1,64}$`)

// Serve exchanges one bounded protocol document without printing private diagnostics.
func Serve(ctx context.Context, engine string, in io.Reader, out io.Writer) error {
	return packagekit.Serve(ctx, Package{Engine: engine}, in, out)
}

// Handle implements plan and apply for native and existing instances.
func (p Package) Handle(ctx context.Context, req workflow.Request) (workflow.Response, error) {
	response := workflow.Response{Version: 1}
	c, err := p.validate(req)
	if err != nil { // Require successful completion before using the returned engine state.
		return response, err
	}
	// Planning is read-only and creates protected execution credentials.
	if req.Action == "plan" {
		prepared, err := p.plan(ctx, req, c)
		response.Prepared = &prepared
		return response, err
	}
	var prepared payload
	if json.Unmarshal(req.Prepared, &prepared) != nil || prepared.Fingerprint != fingerprint(p.Engine, req) { // Reject the operation: engine approval does not match request.
		return response, errors.New("engine approval does not match request")
	}
	if err := validatePrepared(req, c, prepared); err != nil { // Require successful completion before using the returned engine state.
		return response, err
	}
	// Recovery observes approved effects without repeating mutations.
	if req.Action == "inspect" || req.Action == "reconcile" {
		observation, err := p.inspect(ctx, req, c, prepared)
		response.Inspection = &observation
		return response, err
	}
	result, err := p.apply(ctx, req, c, prepared)
	response.Result = &result
	return response, err
}

// validate rejects unused fields and unsupported engine modes before execution.
func (p Package) validate(req workflow.Request) (Config, error) {
	var c Config
	raw, _ := json.Marshal(req.Step.Config)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil { // Reject the operation: invalid engine configuration.
		return c, errors.New("invalid engine configuration")
	}
	if req.Version != 1 || req.Key == "" || !validAction(req.Action) { // Reject the operation: invalid engine protocol.
		return c, errors.New("invalid engine protocol")
	}
	if p.Engine != "postgres" && p.Engine != "redis" { // Reject the operation: engine workflow unsupported.
		return c, errors.New("engine workflow unsupported")
	}
	if c.Mode == "new-instance" { // Route local instance creation through the owned runtime manager.
		return c, validateLocal(req, c)
	}
	// An omitted PostgreSQL name is scoped to this retained workflow identity.
	// Explicit names remain available when database identity must outlive an app rename.
	if p.Engine == "postgres" && c.Mode == "new-database" && c.Database == "" {
		if req.Step.Phase != "" {
			return c, errors.New("derived postgres database requires a retained workflow step")
		}
		c.Database = identity(req)
	}
	return c, p.validateExisting(req, c)
}

// validateLocal requires trusted server context and the native manager's database contract.
func validateLocal(req workflow.Request, c Config) error {
	if req.Host == nil || req.Host.StateDir == "" { // Reject the operation: trusted host context required.
		return errors.New("trusted host context required")
	}
	if c.KeyPrefix != "" || (c.Database != "" && c.Database != "app") { // Reject the operation: local instance database must be app.
		return errors.New("local instance database must be app")
	}
	return nil
}

// validateExisting checks public intent and protected administrative connectivity.
func (p Package) validateExisting(req workflow.Request, c Config) error {
	if c.Storage != "" { // Reject the operation: storage belongs to new-instance.
		return errors.New("storage belongs to new-instance")
	}
	if err := validateRemoteMode(p.Engine, c); err != nil { // Require successful completion before using the returned engine state.
		return err
	}
	// Declared upstream outputs are unavailable until the dependency applies.
	if req.Action == "plan" && req.Step.Inputs["connection"] != "" && req.Inputs["connection"] == "" {
		return nil
	}
	_, err := connection(p.Engine, req)
	return err
}

// fingerprint binds protected execution state to host, configuration and private inputs.
func fingerprint(engine string, req workflow.Request) string {
	req.Action = ""
	req.Prepared = nil
	req.Previous = nil
	// Bind declared input references; their private values arrive only during apply.
	req.Inputs = boundInputs(req)
	data, _ := json.Marshal(req)
	sum := sha256.Sum256(append([]byte(engine), data...))
	return hex.EncodeToString(sum[:])
}

// identity derives a portable stable resource and role identity.
func identity(req workflow.Request) string {
	sum := sha256.Sum256([]byte(req.Key))
	return "hl_" + hex.EncodeToString(sum[:16])
}

// localName reserves room for the manager ownership prefix within a 63-byte DNS label.
func localName(req workflow.Request) string { return "hl-" + identity(req)[3:27] }

// credentials generates an opaque password before any apply mutation.
func credentials(req workflow.Request) (model.ResourceCredentials, error) {
	secret := make([]byte, 32)
	_, err := rand.Read(secret)
	return model.ResourceCredentials{Username: identity(req), Password: hex.EncodeToString(secret), Database: "app"}, err
}

// plan validates local resources and snapshots stable credentials in protected payload.
func (p Package) plan(ctx context.Context, req workflow.Request, c Config) (workflow.Prepared, error) {
	creds, err := credentials(req)
	if err != nil { // Require successful completion before using the returned engine state.
		return workflow.Prepared{}, err
	}
	// Retain successful credentials across reviewed redeploys.
	if len(req.Previous) > 0 {
		var prior payload
		if json.Unmarshal(req.Previous, &prior) != nil || prior.Credentials.Password == "" || prior.Fingerprint != fingerprint(p.Engine, req) { // Reject the operation: invalid previous engine state.
			return workflow.Prepared{}, errors.New("invalid previous engine state")
		}
		creds = prior.Credentials
	}
	if c.Mode == "new-instance" { // Route local instance creation through the owned runtime manager.
		creds.Username = "hostlier"
		if err = p.localPlan(ctx, req, c); err != nil { // Require successful completion before using the returned engine state.
			return workflow.Prepared{}, err
		}
	}
	if p.Engine == "postgres" && c.Mode == "new-database" {
		if creds.Database != "app" && creds.Database != c.Database {
			return workflow.Prepared{}, errors.New("previous postgres database differs from reviewed configuration")
		}
		creds.Database = c.Database
	}
	value := payload{Fingerprint: fingerprint(p.Engine, req), Credentials: creds}
	raw, _ := json.Marshal(value)
	summary := "Provision owned " + p.Engine + " access; retain data on app deletion"
	if p.Engine == "postgres" && c.Mode == "new-database" {
		summary = "Provision PostgreSQL database " + c.Database + " with an owned role; retain data on app deletion"
	}
	return workflow.Prepared{Digest: value.Fingerprint, Summary: []string{summary}, Payload: raw}, nil
}

// manager preserves the server state root for owner-scoped network identities.
func manager(req workflow.Request) (model.ResourceManager, error) {
	return resources.New(resources.Options{Type: req.Host.Runtime, StateDir: req.Host.StateDir})
}

// spec maps the small workflow config onto native resource provisioning.
func spec(engine string, req workflow.Request, c Config) model.ResourceSpec {
	storage := c.Storage
	if storage == "" { // Validate this boundary before advancing the engine workflow.
		storage = "1Gi"
	}
	return model.ResourceSpec{Name: localName(req), Type: engine, Storage: storage}
}

// localPlan is read-only and validates native immutable settings.
func (p Package) localPlan(ctx context.Context, req workflow.Request, c Config) error {
	m, err := manager(req)
	if err != nil { // Require successful completion before using the returned engine state.
		return err
	}
	_, err = m.Plan(ctx, spec(p.Engine, req, c))
	return err
}

// apply performs only the reviewed native or scoped remote operation.
func (p Package) apply(ctx context.Context, req workflow.Request, c Config, prepared payload) (workflow.Result, error) {
	if c.Mode == "new-instance" { // Route local instance creation through the owned runtime manager.
		return p.localApply(ctx, req, c, prepared)
	}
	if p.Engine == "postgres" { // Select the native engine protocol and its isolation rules.
		return postgresApply(ctx, req, c, prepared)
	}
	return redisApply(ctx, req, c, prepared)
}

// localApply waits for authenticated native health and exports its owned network.
func (p Package) localApply(ctx context.Context, req workflow.Request, c Config, prepared payload) (workflow.Result, error) {
	lock, err := resourceLock(req.Host.StateDir)
	if err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	m, err := manager(req)
	if err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	resource, err := m.Provision(ctx, spec(p.Engine, req, c), model.ResourceContext{Credentials: prepared.Credentials, OperationID: req.Key})
	if err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	outputs, err := m.Connection(ctx, resource.Name, prepared.Credentials)
	if err != nil { // Require successful completion before using the returned engine state.
		return workflow.Result{}, err
	}
	enrich(outputs, prepared.Credentials)
	// Redis has a single private instance rather than an app-named SQL database.
	if p.Engine == "redis" {
		delete(outputs, "database")
	}
	state, _ := json.Marshal(prepared)
	return workflow.Result{State: state, Outputs: outputs, Networks: []string{resource.Network}}, nil
}

// enrich exports useful private connection fields alongside the escaped URL.
func enrich(outputs map[string]string, c model.ResourceCredentials) {
	u, _ := url.Parse(outputs["url"])
	outputs["host"] = u.Hostname()
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "postgres", "postgresql":
			port = "5432"
		case "redis", "rediss":
			port = "6379"
		}
	}
	outputs["port"] = port
	outputs["username"] = c.Username
	outputs["password"] = c.Password
	outputs["database"] = c.Database
}

// connection requires verified TLS and rejects competing private aliases.
func connection(engine string, req workflow.Request) (*url.URL, error) {
	raw := req.Secrets["connection"]
	other := req.Inputs["connection"]
	if raw != "" && other != "" { // Reject the operation: connection alias is ambiguous.
		return nil, errors.New("connection alias is ambiguous")
	}
	if raw == "" { // Resolve the connection from its single private input alias.
		raw = other
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User == nil { // Reject the operation: private connection URL required.
		return nil, errors.New("private connection URL required")
	}
	if err := validateConnectionTLS(engine, u); err != nil { // Require successful completion before using the returned engine state.
		return nil, err
	}
	return u, nil
}

// resourceLock serializes native metadata mutations with resource RPC processes.
func resourceLock(directory string) (*os.File, error) {
	if err := os.MkdirAll(directory, 0700); err != nil { // Require successful completion before using the returned engine state.
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, ".resource-package.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil { // Require successful completion before using the returned engine state.
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil { // Require successful completion before using the returned engine state.
		file.Close()
		return nil, err
	}
	return file, nil
}

// validatePrepared protects SQL literals and resource identities at the executable trust boundary.
func validatePrepared(req workflow.Request, c Config, p payload) error {
	expected := identity(req)
	if c.Mode == "new-instance" { // Route local instance creation through the owned runtime manager.
		expected = "hostlier"
	}
	password, err := hex.DecodeString(p.Credentials.Password)
	if err != nil || len(password) != 32 || p.Credentials.Username != expected { // Reject the operation: invalid protected engine credentials.
		return errors.New("invalid protected engine credentials")
	}
	if c.Mode == "new-database" && p.Credentials.Database != c.Database {
		return errors.New("prepared postgres database differs from reviewed configuration")
	}
	return nil
}

// boundInputs excludes deferred values whose immutable references are already in Step.Inputs.
func boundInputs(req workflow.Request) map[string]string {
	result := map[string]string{}
	for key, value := range req.Inputs { // Check each configured engine, namespace, or command independently.
		if req.Step.Inputs[key] == "" { // Retain direct values only when no deferred input reference owns the alias.
			result[key] = value
		}
	}
	return result
}

// validateRemoteMode keeps named PostgreSQL databases distinct from Redis key namespaces.
func validateRemoteMode(engine string, c Config) error {
	// PostgreSQL uses a portable explicit database and never consumes Redis settings.
	if engine == "postgres" {
		return validatePostgresMode(c)
	}
	// Redis has no independent numbered database isolation mode.
	if c.Mode != "existing" || c.Database != "" {
		return errors.New("redis requires existing mode without database")
	}
	// Glob metacharacters cannot enter an ACL pattern through a caller prefix.
	if c.KeyPrefix != "" && !prefix.MatchString(c.KeyPrefix) {
		return errors.New("redis requires a portable keyPrefix")
	}
	return nil
}

// validatePostgresMode rejects fields belonging to native instance creation or cache isolation.
func validatePostgresMode(c Config) error {
	if c.Mode != "new-database" || !identifier.MatchString(c.Database) || c.KeyPrefix != "" { // Reject the operation: postgres requires new-database and a portable database name.
		return errors.New("postgres requires new-database and a portable database name")
	}
	return nil
}

// validateConnectionTLS limits accepted URLs to verified engine protocols.
func validateConnectionTLS(engine string, u *url.URL) error {
	if engine == "postgres" { // Reject the operation: postgres connection requires sslmode=verify-full.
		if (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Query().Get("sslmode") != "verify-full" { // Reject the operation: postgres connection requires sslmode=verify-full.
			return errors.New("postgres connection requires sslmode=verify-full")
		}
		return validatePostgresQuery(u)
	}
	if u.Scheme != "rediss" { // Reject the operation: redis connection requires rediss verified TLS.
		return errors.New("redis connection requires rediss verified TLS")
	}
	return nil
}

// validatePostgresQuery permits only verified TLS, required channel binding, and connection timing.
func validatePostgresQuery(u *url.URL) error {
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("postgres URL contains invalid connection options")
	}
	for key, values := range query {
		// Duplicate options can make the provider and Hostlier choose different values.
		if len(values) != 1 {
			return errors.New("postgres URL contains duplicate connection options")
		}
		switch key {
		case "sslmode":
			if values[0] != "verify-full" {
				return errors.New("postgres connection requires sslmode=verify-full")
			}
		case "channel_binding":
			if values[0] != "require" {
				return errors.New("postgres channel binding must be required")
			}
		case "connect_timeout":
			// Existing connection timing behavior remains available.
		default:
			// Provider credentials, host overrides and local certificate paths are not app configuration.
			return errors.New("postgres URL contains unsupported connection options; use private caCertificatePEM for trust")
		}
	}
	return nil
}

// validAction keeps protocol lifecycle admission separate from engine configuration validation.
func validAction(action string) bool {
	// Every accepted action has a bounded schema and explicit mutation or observation semantics.
	switch action {
	// These explicit lifecycle operations are supported by protocol version one.
	case "plan", "apply", "inspect", "reconcile":
		return true
	}
	return false
}
