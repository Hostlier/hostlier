package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
	"hostlier/internal/store"
)

// HostRecord is the fleet enrollment persisted by the master.
type HostRecord = fleet.HostRecord

// fleetBinding is attached only to internally replayed, human-approved remote requests.
type fleetBinding struct {
	Generation  int64  `json:"generation"`
	Fingerprint string `json:"fingerprint"`
}

// fleetContext separates authenticated controller delegation from caller-controlled HTTP headers.
type fleetContext struct{}

// hostAuthority carries the proposing actor and optional expected host generation across verified mTLS.
type hostAuthority struct {
	Peer       *masterPeer
	Actor      string
	Generation *int64
}

// initializeFleet keeps transport private keys encrypted alongside other controller secrets.
func (c *Controller) initializeFleet() error {
	// Apply this check before granting authority or advancing protocol state.
	if c.state.Hosts == nil {
		c.state.Hosts = map[string]HostRecord{}
	}
	// Reuse the persisted host identity so restarts do not invalidate peer pins.
	if c.state.FleetIdentity.Certificate != "" {
		return nil
	}
	identity, err := fleet.Generate()
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	encrypted, err := c.store.Encrypt(identity.PrivateKey)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	c.state.FleetIdentity = fleet.Identity{Certificate: identity.Certificate, PrivateKey: encrypted}
	return nil
}

// fleetIdentity decrypts a private transport key only for constructing a TLS connection.
func (c *Controller) fleetIdentity() (fleet.Identity, error) {
	key, err := c.store.Decrypt(c.state.FleetIdentity.PrivateKey)
	return fleet.Identity{Certificate: c.state.FleetIdentity.Certificate, PrivateKey: key}, err
}

// apiHosts manages approved host enrollment and dispatches explicitly targeted workload requests.
func (c *Controller) apiHosts(r *http.Request, p []string, actor string) (any, error) {
	// Apply this check before granting authority or advancing protocol state.
	if _, inner, ok := model.FleetPath(r.URL.Path); ok {
		return c.proxyHost(r, p[2], inner, actor)
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "GET", 2, "") {
		hosts, err := collectionMap[HostRecord](r.Context(), c.store, "hosts", store.CollectionQuery{})
		return map[string]any{"hosts": hosts}, err
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "GET", 3, "identity") {
		pin, err := fleet.Fingerprint(c.state.FleetIdentity.Certificate)
		return map[string]string{"certificate": c.state.FleetIdentity.Certificate, "fingerprint": pin}, err
	}
	// Apply this check before granting authority or advancing protocol state.
	if matches(r, p, "POST", 2, "") {
		return c.enrollHost(r)
	}
	// Host removal revokes the enrolled identity and its dependent grants.
	if matches(r, p, "DELETE", 3, "") {
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err := emptyBody(r); err != nil {
			return nil, err
		}
		// Apply this check before granting authority or advancing protocol state.
		if _, ok := c.state.Hosts[p[2]]; !ok {
			return endpointNotFound()
		}
		delete(c.state.Hosts, p[2])
		// Evaluate each retained grant without expanding its signed scope.
		for _, grant := range c.state.Authorizations {
			// Apply this check before granting authority or advancing protocol state.
			if grant.Host == p[2] {
				grant.Revoked = true
			}
		}
		c.state.Generation++
		c.log("Host enrollment revoked: "+p[2], "", "warn")
		return map[string]bool{"revoked": true}, nil
	}
	return endpointNotFound()
}

// enrollHost adds an exact identity; replacing a host requires explicit revocation and fresh approval.
func (c *Controller) enrollHost(r *http.Request) (any, error) {
	var host HostRecord
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := decode(r, &host); err != nil {
		return nil, err
	}
	// Reject the request before side effects: Host URL must be an HTTPS origin.
	if !validHostURL(host.URL) {
		return nil, model.Err("INVALID_HOST", "Host URL must be an HTTPS origin")
	}
	// Reject the request before side effects: Host requires a simple name and exact SHA-256 certificate fingerprint.
	if !fleet.ValidHostName(host.Name) || !fleet.ValidPin(host.Fingerprint) {
		return nil, model.Err("INVALID_HOST", "Host requires a simple name and exact SHA-256 certificate fingerprint")
	}
	// Reject the request before side effects: Revoke the old enrollment before replacing its identity.
	if _, exists := c.state.Hosts[host.Name]; exists {
		return nil, model.Err("HOST_EXISTS", "Revoke the old enrollment before replacing its identity")
	}
	// Reject the request before side effects: Host enrollment limit reached.
	if len(c.state.Hosts) >= 1000 {
		return nil, model.Err("HOST_LIMIT", "Host enrollment limit reached")
	}
	c.state.Hosts[host.Name] = host
	c.state.Generation++
	c.log("Host enrolled: "+host.Name+" identity "+host.Fingerprint, "", "info")
	return host, nil
}

// proxyHost delegates only workload APIs and checks the retained target identity before execution.
func (c *Controller) proxyHost(r *http.Request, id, inner, actor string) (any, error) {
	host, ok := c.state.Hosts[id]
	// Apply this check before granting authority or advancing protocol state.
	if !ok || !model.FleetRoute(inner) {
		return endpointNotFound()
	}
	binding, _ := r.Context().Value(fleetContext{}).(*fleetBinding)
	// Reject the request before side effects: Host mutation requires review of its current identity and state.
	if model.RequiresHumanApproval(r.Method, inner) && (binding == nil || binding.Fingerprint != host.Fingerprint) {
		return nil, model.Err("HUMAN_APPROVAL_REQUIRED", "Host mutation requires review of its current identity and state")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	// Reject the request before side effects: Host request exceeds its bounded limit.
	if err != nil || len(body) > maxRequestBytes {
		return nil, model.Err("INVALID_REQUEST", "Host request exceeds its bounded limit")
	}
	result, err := c.fleetRequest(r.Context(), host, r.Method, fleetForwardPath(r, inner), body, actor, binding)
	// Propagate failure before performing the next recovery side effect.
	if err == nil {
		err = c.trackRemoteBackupPolicy(r, host, inner, result)
	}
	return result, err
}

// fleetRequest pins both peers, preserves actor identity and never retries an ambiguous mutation.
func (c *Controller) fleetRequest(ctx context.Context, host HostRecord, method, path string, body []byte, actor string, binding *fleetBinding) (any, error) {
	identity, err := c.fleetIdentity()
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return nil, err
	}
	config, err := fleet.TLS(identity, host.Fingerprint, false)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, host.URL+path, bytes.NewReader(body))
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Hostlier-Actor", strings.TrimPrefix(c.store.Authenticate("fleet-actor|"+actor), "hmac:"))
	// Apply this check before granting authority or advancing protocol state.
	if binding != nil {
		data, _ := json.Marshal(binding.Generation)
		request.Header.Set("X-Hostlier-Generation", string(data))
	}
	transport := &http.Transport{TLSClientConfig: config, TLSHandshakeTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	// Keep this callback bounded to the current request and its captured state.
	client := &http.Client{Transport: transport, Timeout: c.opts.LongOperationTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Network work must not block central approval, status or other hosts.
	c.mu.Unlock()
	defer c.mu.Lock()
	response, err := client.Do(request)
	// Reject the request before side effects: Enrolled host unreachable; inspect its operations before retrying a mutation.
	if err != nil {
		return nil, model.Err("HOST_UNAVAILABLE", "Enrolled host unreachable; inspect its operations before retrying a mutation")
	}
	defer response.Body.Close()
	return decodeHostResponse(response)
}

// bindHostIntent captures target identity and state before private intent is encrypted and reviewed.
func (c *Controller) bindHostIntent(request *humanRequest, actor string) error {
	id, _, ok := model.FleetPath(request.Path)
	// Local intent has no delegated host identity to bind.
	if !ok {
		request.Target = nil
		return nil
	}
	host, ok := c.state.Hosts[id]
	// Reject the request before side effects: Enrolled host not found.
	if !ok {
		return model.Err("NOT_FOUND", "Enrolled host not found")
	}
	result, err := c.fleetRequest(c.ctx, host, "GET", "/v1/host-state", nil, actor, nil)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	data, _ := json.Marshal(result)
	var state struct {
		Generation *int64 `json:"generation"`
	}
	// Reject the request before side effects: Host does not expose a generation-bound execution protocol.
	if json.Unmarshal(data, &state) != nil || state.Generation == nil {
		return model.Err("HOST_PROTOCOL", "Host does not expose a generation-bound execution protocol")
	}
	request.Target = &fleetBinding{Generation: *state.Generation, Fingerprint: host.Fingerprint}
	return nil
}

// reviewHostIntent displays verified host identity and retrieves actor-owned plans from that host.
func (c *Controller) reviewHostIntent(request humanRequest, actor string, review map[string]any) (json.RawMessage, error) {
	id, inner, _ := model.FleetPath(request.Path)
	host, ok := c.state.Hosts[id]
	// Reject the request before side effects: Host identity changed; request a new review.
	if !ok || request.Target == nil || host.Fingerprint != request.Target.Fingerprint {
		return nil, model.Err("STALE_PLAN", "Host identity changed; request a new review")
	}
	review["host"], review["hostGeneration"] = host, request.Target.Generation
	// Propagate failure before performing the next recovery side effect.
	if err := c.reviewHostRecovery(request, host, inner, actor, review); err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(inner, "/"), "/")
	// Apply this check before granting authority or advancing protocol state.
	if len(parts) == 4 && parts[1] == "plans" && parts[3] == "apply" {
		result, err := c.fleetRequest(c.ctx, host, "GET", "/v1/plans/"+parts[2], nil, actor, nil)
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err != nil {
			return nil, err
		}
		// Reject the request before side effects: Host plan changed or expired; prepare a new plan.
		if !c.validHostPlan(result, request) {
			return nil, model.Err("STALE_PLAN", "Host plan changed or expired; prepare a new plan")
		}
		review["plan"] = result
	}
	return json.Marshal(review)
}

// validHostURL prevents enrollment from smuggling credentials, query parameters or alternate paths.
func validHostURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

// decodeHostResponse bounds peer data and preserves structured errors without accepting ambiguous success.
func decodeHostResponse(response *http.Response) (any, error) {
	data, err := io.ReadAll(io.LimitReader(response.Body, (96<<20)+1))
	// Reject the request before side effects: Host response incomplete or too large; inspect execution state.
	if err != nil || len(data) > 96<<20 {
		return nil, model.Err("HOST_RESPONSE", "Host response incomplete or too large; inspect execution state")
	}
	var result any
	// Reject the request before side effects: Host returned an invalid response.
	if json.Unmarshal(data, &result) != nil {
		return nil, model.Err("HOST_RESPONSE", "Host returned an invalid response")
	}
	// Advance only from the expected retained protocol state.
	if response.StatusCode != http.StatusOK {
		var envelope struct {
			Error *model.Error `json:"error"`
		}
		json.Unmarshal(data, &envelope)
		// Reject the request before side effects: Host rejected the request.
		if envelope.Error != nil {
			return nil, envelope.Error
		}
		return nil, model.Err("HOST_RESPONSE", "Host rejected the request")
	}
	return result, nil
}

// validHostPlan verifies the retained host plan fingerprint, generation and expiry before presentation.
func (c *Controller) validHostPlan(result any, request humanRequest) bool {
	data, _ := json.Marshal(result)
	var plan Plan
	var supplied approvalRequest
	return json.Unmarshal(data, &plan) == nil && json.Unmarshal(request.Body, &supplied) == nil && supplied.Fingerprint == plan.Fingerprint && plan.Generation == request.Target.Generation && plan.ExpiresAt > c.opts.Now().UnixMilli()
}

// fleetForwardPath preserves bounded history filters without changing approved mutation paths.
func fleetForwardPath(r *http.Request, path string) string {
	// Preserve read-only history and log selectors when delegating to the box.
	if r.Method == http.MethodGet && (path == "/v1/telemetry/history" || path == "/v1/logs" || strings.HasPrefix(path, "/v1/apps/") && strings.HasSuffix(path, "/logs")) && r.URL.RawQuery != "" {
		return path + "?" + r.URL.RawQuery
	}
	return path
}
