package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"hostlier/pkg/workflow"
)

// service contains only the official service fields required for ownership and readiness.
type service struct {
	Name    string            `json:"service_name"`
	Type    string            `json:"service_type"`
	Created string            `json:"create_time"`
	URI     string            `json:"service_uri"`
	State   string            `json:"state"`
	Cloud   string            `json:"cloud_name"`
	Plan    string            `json:"plan"`
	Tags    map[string]string `json:"tags"`
}

// owned records the immutable creation timestamp as well as the provider-scoped name.
type owned struct {
	Name    string
	Created string
	Owner   string
}

// approval pins the read-only placement and pricing snapshot to the reviewed request.
type approval struct {
	Fingerprint string
	Snapshot    json.RawMessage
	Existing    *owned
}

// snapshot combines authoritative plan capacity and regional price without credentials.
type snapshot struct {
	Price  price
	DiskMB int `json:"disk_space_mb"`
	Nodes  int `json:"node_count"`
}

// price mirrors the required fields of ProjectServicePlanPriceGetResponse.
type price struct {
	Hourly string `json:"base_price_usd"`
	Cloud  string `json:"cloud_name"`
	Plan   string `json:"service_plan"`
	Type   string `json:"service_type"`
}

var label = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// validateAiven bounds path segments and rejects ignored credentials and engine options.
func validateAiven(req workflow.Request, c config) error {
	// Reject invalid input: direct Aiven new-instance supports postgres and valkey only.
	if c.Engine != "postgres" && c.Engine != "valkey" {
		return errors.New("direct Aiven new-instance supports postgres and valkey only")
	}
	// Reject invalid input: new-instance requires instance placement; create scoped credentials in a subsequent engine step.
	if c.Database != "" || c.KeyPrefix != "" || c.Instance == nil {
		return errors.New("new-instance requires instance placement; create scoped credentials in a subsequent engine step")
	}
	// Stop before producing state when the provider operation fails.
	if err := validatePlacement(c.Instance); err != nil {
		return err
	}
	return validateToken(req)
}

// serviceType maps Hostlier's engine name to the official Aiven service type code.
func serviceType(c config) string {
	// Validate provider state before advancing the workflow.
	if c.Engine == "postgres" {
		return "pg"
	}
	return "valkey"
}

// owner derives a stable non-secret provider tag from the controller's resource identity.
func owner(req workflow.Request) string {
	sum := sha256.Sum256([]byte(req.Key))
	return hex.EncodeToString(sum[:])
}

// base scopes all requests to an explicitly selected project.
func base(c config) string { return "/project/" + url.PathEscape(c.Instance.Project) }

// aiven prepares and executes one reviewed Aiven service creation.
func (p Package) aiven(ctx context.Context, req workflow.Request, c config) (workflow.Response, error) {
	response := workflow.Response{Version: 1}
	// Stop before producing state when the provider operation fails.
	if err := validateAiven(req, c); err != nil {
		return response, err
	}
	current, err := p.preflight(ctx, req, c)
	// Stop before producing state when the provider operation fails.
	if err != nil {
		return response, err
	}
	// Return the read-only snapshot for explicit review.
	if req.Action == "plan" {
		raw, _ := json.Marshal(current)
		response.Prepared = &workflow.Prepared{Payload: raw, Summary: []string{fmt.Sprintf("Aiven %s %s/%s in %s, plan %s", c.Engine, c.Instance.Project, c.Instance.Service, c.Instance.Cloud, c.Instance.Plan), "Paid service; recurring regional base price and storage capacity are pinned in the private approval snapshot. No service deletion is automated.", "Administrative connection is private; a subsequent engine step must create scoped application credentials."}}
		var s snapshot
		_ = json.Unmarshal(current.Snapshot, &s)
		response.Prepared.Summary = append(response.Prepared.Summary, fmt.Sprintf("Base USD/hour %s; included disk %d MiB; nodes %d; extras and taxes may apply", s.Price.Hourly, s.DiskMB, s.Nodes))
		return response, nil
	}
	var approved approval
	// Validate provider state before advancing the workflow.
	if json.Unmarshal(req.Prepared, &approved) != nil || approved.Fingerprint != current.Fingerprint || string(approved.Snapshot) != string(current.Snapshot) || !sameOwnership(approved.Existing, current.Existing) {
		return response, errors.New("Aiven plan changed; obtain a new approval")
	}
	result, err := p.provision(ctx, req, c, current.Existing)
	// Stop before producing state when the provider operation fails.
	if err != nil {
		return response, err
	}
	response.Result = &result
	return response, nil
}

// sameOwnership rejects appearance, replacement, or disappearance after approval.
func sameOwnership(a, b *owned) bool {
	// Validate provider state before advancing the workflow.
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// preflight is strictly read-only, verifies ownership, and resolves authoritative price and capacity.
func (p Package) preflight(ctx context.Context, req workflow.Request, c config) (approval, error) {
	a := approval{Fingerprint: fingerprint(req)}
	prior, err := p.existingService(ctx, req, c)
	// Stop before producing state when the provider operation fails.
	if err != nil {
		return a, err
	}
	a.Existing = prior
	token := req.Secrets["apiToken"]
	i := c.Instance
	t := serviceType(c)
	var s snapshot
	_, err = p.Client.call(ctx, http.MethodGet, base(c)+"/pricing/service-types/"+t+"/plans/"+i.Plan+"/clouds/"+i.Cloud, token, nil, &s.Price)
	// Stop before producing state when the provider operation fails.
	if err != nil {
		return a, errors.New("Aiven regional pricing preflight failed")
	}
	var specs struct {
		Disk  int    `json:"disk_space_mb"`
		Nodes int    `json:"node_count"`
		Plan  string `json:"service_plan"`
		Type  string `json:"service_type"`
	}
	_, err = p.Client.call(ctx, http.MethodGet, base(c)+"/service-types/"+t+"/plans/"+i.Plan, token, nil, &specs)
	// Stop before producing state when the provider operation fails.
	if err != nil {
		return a, errors.New("Aiven plan capacity preflight failed")
	}
	// Validate provider state before advancing the workflow.
	if !validPrice(s.Price, i, t) || !validCapacity(specs.Disk, specs.Nodes, specs.Plan, specs.Type, i, t) {
		return a, errors.New("Aiven plan does not prove persistent capacity and regional availability")
	}
	s.DiskMB = specs.Disk
	s.Nodes = specs.Nodes
	a.Snapshot, _ = json.Marshal(s)
	return a, nil
}

// existingService refuses adoption by name, even when a resource bears a matching tag.
func (p Package) existingService(ctx context.Context, req workflow.Request, c config) (*owned, error) {
	var out struct {
		Service service `json:"service"`
	}
	status, err := p.Client.call(ctx, http.MethodGet, base(c)+"/service/"+c.Instance.Service, req.Secrets["apiToken"], nil, &out)
	// Validate provider state before advancing the workflow.
	if status == http.StatusNotFound && len(req.Previous) == 0 {
		return nil, nil
	}
	// Stop before producing state when the provider operation fails.
	if err != nil {
		return nil, errors.New("Aiven existing service preflight failed")
	}
	var prior owned
	// Validate provider state before advancing the workflow.
	if json.Unmarshal(req.Previous, &prior) != nil || prior.Owner != owner(req) || prior.Name != c.Instance.Service || prior.Created == "" {
		return nil, errors.New("Aiven service exists without durable Hostlier ownership; refusing adoption")
	}
	// Validate provider state before advancing the workflow.
	if !matches(out.Service, c, prior) {
		return nil, errors.New("Aiven owned service identity or placement changed")
	}
	return &prior, nil
}

// matches binds reuse to the persisted creation identity, owner tag, engine and placement.
func matches(s service, c config, o owned) bool {
	return s.Name == o.Name && s.Created == o.Created && s.Tags["hostlier-owner"] == o.Owner && s.Type == serviceType(c) && s.Cloud == c.Instance.Cloud && s.Plan == c.Instance.Plan
}

// provision performs at most one create, then only read-only readiness checks.
func (p Package) provision(ctx context.Context, req workflow.Request, c config, prior *owned) (workflow.Result, error) {
	// Create only when the reviewed plan proved the service absent.
	if prior == nil {
		body := map[string]any{"service_name": c.Instance.Service, "service_type": serviceType(c), "plan": c.Instance.Plan, "cloud": c.Instance.Cloud, "termination_protection": true, "tags": map[string]string{"hostlier-owner": owner(req)}}
		// Explicit Valkey settings retain encrypted transport and backup persistence.
		if c.Engine == "valkey" {
			body["user_config"] = map[string]any{"valkey_ssl": true, "valkey_persistence": "rdb"}
		}
		var out struct {
			Service service `json:"service"`
		}
		_, err := p.Client.call(ctx, http.MethodPost, base(c)+"/service", req.Secrets["apiToken"], body, &out)
		// Stop before producing state when the provider operation fails.
		if err != nil {
			return workflow.Result{}, err
		}
		prior = &owned{Name: out.Service.Name, Created: out.Service.Created, Owner: owner(req)}
		// Validate provider state before advancing the workflow.
		if prior.Created == "" || !matches(out.Service, c, *prior) {
			return workflow.Result{}, errors.New("Aiven creation response requires manual reconciliation")
		}
	}
	return p.waitReady(ctx, req, c, *prior)
}

// waitReady polls without retrying creation and leaves uncertain executions for manual reconciliation.
func (p Package) waitReady(ctx context.Context, req workflow.Request, c config, o owned) (workflow.Result, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Polling reads may repeat; provider mutations never do.
	for {
		var out struct {
			Service service `json:"service"`
		}
		_, err := p.Client.call(bounded, http.MethodGet, base(c)+"/service/"+o.Name, req.Secrets["apiToken"], nil, &out)
		// Stop before producing state when the provider operation fails.
		if err != nil {
			return workflow.Result{}, err
		}
		// Validate provider state before advancing the workflow.
		if !matches(out.Service, c, o) {
			return workflow.Result{}, errors.New("Aiven service ownership changed while waiting")
		}
		// Publish private connection material only after the service is running.
		if out.Service.State == "RUNNING" {
			return p.result(bounded, req, c, o, out.Service)
		}
		// Validate provider state before advancing the workflow.
		select {
		// Wait for readiness without repeating the create mutation.
		case <-bounded.Done():
			return workflow.Result{}, errors.New("Aiven service not ready; reconcile existing service before retry")
		// Wait for readiness without repeating the create mutation.
		case <-time.After(2 * time.Second):
		}
	}
}

// result returns administrative material only through the controller's non-bindable output channel.
func (p Package) result(ctx context.Context, req workflow.Request, c config, o owned, s service) (workflow.Result, error) {
	u, err := url.Parse(s.URI)
	// Validate provider state before advancing the workflow.
	if !validURI(u, err) {
		return workflow.Result{}, errors.New("Aiven ready service has no usable connection")
	}
	// Validate provider state before advancing the workflow.
	if c.Engine == "postgres" {
		// Validate provider state before advancing the workflow.
		if u.Scheme != "postgres" && u.Scheme != "postgresql" {
			return workflow.Result{}, errors.New("Aiven PostgreSQL URI invalid")
		}
		q := u.Query()
		q.Set("sslmode", "verify-full")
		u.RawQuery = q.Encode()
	} else {
		// Validate provider state before advancing the workflow.
		if u.Scheme == "valkeys" {
			u.Scheme = "rediss"
		}
		// Validate provider state before advancing the workflow.
		if u.Scheme != "rediss" {
			return workflow.Result{}, errors.New("Aiven Valkey must expose authenticated TLS")
		}
	}
	var ca struct {
		Certificate string `json:"certificate"`
	}
	_, err = p.Client.call(ctx, http.MethodGet, base(c)+"/kms/ca", req.Secrets["apiToken"], nil, &ca)
	// Stop before producing state when the provider operation fails.
	if err != nil || ca.Certificate == "" {
		return workflow.Result{}, errors.New("Aiven project CA unavailable")
	}
	state, _ := json.Marshal(o)
	return workflow.Result{State: state, Outputs: map[string]string{"serviceId": c.Instance.Project + "/" + s.Name, "host": u.Hostname()}, PrivateOutputs: map[string]string{"connection": u.String(), "caCertificatePEM": ca.Certificate}}, nil
}

// validatePlacement rejects unbounded or ambiguous project and placement segments.
func validatePlacement(i *instance) error {
	// Reject invalid input: Aiven requires explicit project, service, plan and cloud identifiers.
	if !label.MatchString(i.Project) || !label.MatchString(i.Service) || !label.MatchString(i.Plan) || !label.MatchString(i.Cloud) {
		return errors.New("Aiven requires explicit project, service, plan and cloud identifiers")
	}
	return nil
}

// validateToken prevents ignored administrative inputs and requires protected API credentials.
func validateToken(req workflow.Request) error {
	// Reject invalid input: Aiven new-instance requires only the apiToken secret.
	if req.Step.Secrets["apiToken"] == "" || req.Secrets["apiToken"] == "" || len(req.Step.Secrets) != 1 || len(req.Inputs) != 0 || len(req.Step.Inputs) != 0 {
		return errors.New("Aiven new-instance requires only the apiToken secret")
	}
	return nil
}

// validPrice requires finite nonnegative cost and the exact requested region and plan.
func validPrice(p price, i *instance, t string) bool {
	n, e := strconv.ParseFloat(p.Hourly, 64)
	return e == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && p.Cloud == i.Cloud && p.Plan == i.Plan && p.Type == t
}

// validCapacity requires documented persistent disk and the requested engine plan.
func validCapacity(disk, nodes int, plan, t string, i *instance, engine string) bool {
	return disk > 0 && nodes > 0 && plan == i.Plan && t == engine
}

// validURI requires an authenticated host before administrative material is returned.
func validURI(u *url.URL, err error) bool { return err == nil && u.Hostname() != "" && u.User != nil }
