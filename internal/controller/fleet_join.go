package controller

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// FleetJoin retains only public proof and a capability verifier across restarts.
type FleetJoin struct {
	Request  fleet.JoinRequest `json:"request"`
	Approved bool              `json:"approved"`
}

// publicFleetJoin permits only request creation and capability-authenticated polling.
func publicFleetJoin(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/v1/fleet-joins/request" || r.URL.Path == "/v1/fleet-joins/poll")
}

// apiFleetJoin keeps member signing local and requires retained authority for remote approval.
func (c *Controller) apiFleetJoin(r *http.Request, p []string, local bool, actor string) (any, error) {
	if publicFleetJoin(r) {
		if p[2] == "request" {
			return c.requestFleetJoin(r)
		}
		return c.pollFleetJoin(r)
	}
	administrator := local && actor == "local" && !c.agentOnly
	if matches(r, p, "POST", 3, "prepare") {
		if c.controlPlaneOnly() {
			return nil, model.Err("CONTROLLER_MODE", "A control-plane controller cannot join as a workload worker")
		}
		if !administrator {
			return nil, model.Err("UNAUTHORIZED", "Local controller administrator required")
		}
		return c.prepareFleetJoin(r)
	}
	approval, _ := r.Context().Value(humanExecutionKey{}).(*HumanApproval)
	if !administrator && (c.agentOnly || (approval == nil || approval.AuthorizationID == "") && !c.canAddServer(actor)) {
		return nil, model.Err("SCOPE_DENIED", "Adding a controller requires servers.add permission")
	}
	if len(p) == 3 && r.Method == http.MethodGet {
		return c.reviewFleetJoin(p[2])
	}
	if len(p) == 4 && p[3] == "approve" && r.Method == http.MethodPost {
		return c.approveFleetJoin(r, p[2])
	}
	return endpointNotFound()
}

// prepareFleetJoin signs one inert request without changing the selected master.
func (c *Controller) prepareFleetJoin(r *http.Request) (any, error) {
	var input fleet.JoinPrepare
	if err := decode(r, &input); err != nil {
		return nil, err
	}
	origin, err := model.ControllerURL(input.URL)
	if err != nil {
		return nil, err
	}
	if input.Name == "" {
		input.Name, _, _ = strings.Cut(strings.TrimPrefix(strings.ToLower(c.state.Hostname), "hostlier."), ".")
	}
	if !fleet.ValidHostName(input.Name) || input.Name == "local" {
		return nil, model.Err("INVALID_HOST", "Choose a valid controller --name")
	}
	identity, err := c.fleetIdentity()
	if err != nil {
		return nil, err
	}
	token := random()
	request, err := fleet.SignJoin(identity, fleet.JoinRequest{MasterURL: origin, Name: input.Name, Hostname: c.state.Hostname, TokenHash: fleet.JoinTokenHash(token), ExpiresAt: c.opts.Now().Add(fleet.JoinTTL).UnixMilli()})
	if err != nil {
		return nil, err
	}
	prepared := fleet.PreparedJoin{Request: request, Token: token, PreviousURL: c.state.MasterURL}
	if c.master.peer != nil {
		prepared.Previous = c.master.peer.pin
	}
	return prepared, nil
}

// requestFleetJoin bounds public state and rejects identity and name conflicts before review.
func (c *Controller) requestFleetJoin(r *http.Request) (any, error) {
	var request fleet.JoinRequest
	if err := decode(r, &request); err != nil {
		return nil, err
	}
	pin, err := fleet.VerifyJoin(request, c.opts.Now())
	if err != nil {
		return nil, model.Err("INVALID_JOIN", "Invalid, expired or unsigned controller join request")
	}
	origin, _ := url.Parse(request.MasterURL)
	if !strings.EqualFold(origin.Hostname(), c.state.Hostname) {
		return nil, model.Err("INVALID_JOIN", "Join using this controller's configured HTTPS hostname")
	}
	ownPin, _ := fleet.Fingerprint(c.state.FleetIdentity.Certificate)
	if pin == ownPin {
		return nil, model.Err("INVALID_HOST", "A controller cannot join itself")
	}
	if prior, ok := c.state.Hosts[request.Name]; ok && prior.Fingerprint != pin {
		return nil, model.Err("HOST_EXISTS", "Fleet name belongs to another identity; choose another name or revoke it first")
	}
	if c.state.FleetJoins == nil {
		c.state.FleetJoins = map[string]*FleetJoin{}
	}
	for code, join := range c.state.FleetJoins {
		if join.Request.ExpiresAt <= c.opts.Now().UnixMilli() {
			delete(c.state.FleetJoins, code)
		}
	}
	code := fleet.JoinCode(request)
	if prior := c.state.FleetJoins[code]; prior != nil {
		if prior.Request != request {
			return nil, model.Err("INVALID_JOIN", "Join code collision; start a new request")
		}
		return c.reviewFleetJoin(code)
	}
	if len(c.state.FleetJoins) >= 100 {
		return nil, model.Err("RATE_LIMITED", "Too many pending controller joins; retry after existing requests expire")
	}
	c.state.FleetJoins[code] = &FleetJoin{Request: request}
	c.log("Controller join requested: "+request.Name+" code "+code, "", "info")
	return c.reviewFleetJoin(code)
}

// reviewFleetJoin never returns polling credentials or a private certificate key.
func (c *Controller) reviewFleetJoin(code string) (fleet.JoinReview, error) {
	join := c.state.FleetJoins[code]
	if join == nil || join.Request.ExpiresAt <= c.opts.Now().UnixMilli() {
		return fleet.JoinReview{}, model.Err("JOIN_EXPIRED", "Controller join request is missing or expired; connect again")
	}
	pin, _ := fleet.Fingerprint(join.Request.Certificate)
	masterPin, _ := fleet.Fingerprint(c.state.FleetIdentity.Certificate)
	session := c.fleetSessions[pin]
	connected := session != nil && session.conn.CanTakeNewRequest()
	return fleet.JoinReview{Code: code, Name: join.Request.Name, Hostname: join.Request.Hostname, Fingerprint: pin, MasterURL: c.fleetEndpoint(), MasterFingerprint: masterPin, ExpiresAt: join.Request.ExpiresAt, Approved: join.Approved, Connected: connected}, nil
}

// pollFleetJoin requires the private capability bound into the member's signed request.
func (c *Controller) pollFleetJoin(r *http.Request) (any, error) {
	var poll fleet.JoinPoll
	if err := decode(r, &poll); err != nil {
		return nil, err
	}
	join := c.state.FleetJoins[poll.Code]
	if join == nil || !fleet.ValidPin(poll.Token) || subtle.ConstantTimeCompare([]byte(fleet.JoinTokenHash(poll.Token)), []byte(join.Request.TokenHash)) != 1 {
		return nil, model.Err("UNAUTHORIZED", "Invalid controller join capability")
	}
	review, err := c.reviewFleetJoin(poll.Code)
	if err != nil {
		return nil, err
	}
	if review.Approved && c.state.Hosts[review.Name].Fingerprint != review.Fingerprint {
		return nil, model.Err("HOST_REVOKED", "Controller enrollment has been revoked")
	}
	return review, nil
}

// approveFleetJoin commits the exact reviewed identity before returning success.
func (c *Controller) approveFleetJoin(r *http.Request, code string) (any, error) {
	var decision fleet.JoinDecision
	if err := decode(r, &decision); err != nil {
		return nil, err
	}
	review, err := c.reviewFleetJoin(code)
	if err != nil {
		return nil, err
	}
	if review.Approved {
		return nil, model.Err("JOIN_APPROVED", "Controller join request was already approved")
	}
	if decision.Fingerprint != review.Fingerprint {
		return nil, model.Err("STALE_PLAN", "Controller identity differs from the reviewed request")
	}
	prior, exists := c.state.Hosts[review.Name]
	if exists && prior.Fingerprint != review.Fingerprint {
		return nil, model.Err("HOST_EXISTS", "Fleet name belongs to another controller")
	}
	if !exists && len(c.state.Hosts) >= 1000 {
		return nil, model.Err("HOST_LIMIT", "Host enrollment limit reached")
	}
	join := c.state.FleetJoins[code]
	if _, err := fleet.VerifyJoin(join.Request, c.opts.Now()); err != nil {
		return nil, model.Err("INVALID_JOIN", "Member identity is no longer valid; connect again")
	}
	if !exists {
		c.state.Hosts[review.Name] = HostRecord{Name: review.Name, Fingerprint: review.Fingerprint}
	}
	join.Approved = true
	c.state.Generation++
	if err := c.store.Save(c.state); err != nil {
		join.Approved = false
		c.state.Generation--
		if !exists {
			delete(c.state.Hosts, review.Name)
		}
		return nil, err
	}
	c.log("Controller join approved: "+review.Name+" identity "+review.Fingerprint, "", "info")
	return c.reviewFleetJoin(code)
}

// fleetEndpoint advertises the configured listener port while retaining the public hostname.
func (c *Controller) fleetEndpoint() string {
	port := fleet.Port
	if c.fleetAddress != "" {
		_, port, _ = net.SplitHostPort(c.fleetAddress)
	}
	return "https://" + net.JoinHostPort(c.state.Hostname, port)
}

// fleetJoinApproval identifies the CLI-only ceremony, which cannot fall back to browser approval.
func fleetJoinApproval(method, path string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	return method == "POST" && len(p) == 4 && p[0] == "v1" && p[1] == "fleet-joins" && p[3] == "approve"
}
