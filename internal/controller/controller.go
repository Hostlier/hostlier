// Package controller implements the authenticated durable Hostlier API.
package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/internal/store"
	"hostlier/internal/tunnel"
)

// random generates an unpredictable credential or identity using the operating system RNG.
func random() string {
	v := make([]byte, 32)
	// Abort credential generation if operating system randomness fails.
	if _, err := rand.Read(v); err != nil {
		panic(err)
	}
	return hex.EncodeToString(v)
}

// verifier hashes a credential so durable state never needs its plaintext.
func verifier(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }

// New validates configuration, opens durable state and recovers interrupted work before serving.
func New(o Options) (*Controller, error) {
	// Reject this path: Runtime required.
	if o.Runtime == nil {
		return nil, model.Err("INVALID_RUNTIME", "Runtime required")
	}
	conf, err := config.ValidateServer(o.Config)
	// Reject an invalid server domain allowlist before changing authoritative state.
	if err != nil {
		return nil, err
	}
	o.Config = conf
	o = defaultOptions(o)
	// Restored ownership must use its original operational state path.
	if err := model.ValidateRecoveryLocation(o.StateDir); err != nil {
		return nil, err
	}
	c := &Controller{opts: o, tunnelBrokers: map[string]*tunnel.Broker{}, rates: map[string]rate{}, deviceEpoch: random(), deviceNonces: map[string]int64{}}
	c.state = State{Version: 2, Runtime: o.Runtime.Name(), Hostname: o.Hostname, Domains: append([]string{}, conf.Domains...), Clients: map[string]*Client{}, Enrollments: map[string]*Enrollment{}, Requests: map[string]*Enrollment{}, Secrets: map[string]*Secret{}, Writes: map[string]Write{}, Plans: map[string]*Plan{}, Operations: map[string]*Operation{}, Applications: map[string]*Application{}}
	c.store, err = store.Open(o.StateDir, &c.state)
	// Acquire valid durable state before starting the controller.
	if err != nil {
		return nil, err
	}
	// Reject this path: State version or runtime incompatible.
	if c.state.Version != 2 || c.state.Runtime != o.Runtime.Name() {
		c.store.Close()
		return nil, model.Err("INCOMPATIBLE_STATE", "State version or runtime incompatible")
	}
	// Reject incompatible persisted state or unauthorized configured routes before serving.
	if err = c.reconcileState(conf); err != nil {
		c.store.Close()
		return nil, err
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	// Every service initialization failure releases the same controller ownership boundary.
	if err = c.initializeServices(); err != nil {
		c.cancel()
		c.store.Close()
		return nil, err
	}
	return c, nil
}

// initializeServices prepares identities and durable recovery before starting controller-owned workers.
func (c *Controller) initializeServices() error {
	// Validate reserved tunnel authority before starting any workers.
	if err := c.initializeTunnels(); err != nil {
		return err
	}
	// Fleet transport identities are distinct from API tokens and human approval authority.
	if err := c.initializeFleet(); err != nil {
		return err
	}
	// Existing client credentials never acquire human authority during migration.
	if err := c.initializeHuman(); err != nil {
		return err
	}
	c.migrateEventSequences()
	c.interruptPendingOperations()
	c.initializePlanning()
	c.initializeRecovery()
	c.log("Controller initialized", "", "info")
	// Require durable state persistence before reporting success or scheduling side effects.
	if err := c.store.Save(c.state); err != nil {
		return err
	}
	c.eventPersistedSequence = c.state.EventSequence
	// Telemetry owns private storage and workers before any other monitor starts.
	if err := c.startStatusCapture(); err != nil {
		return err
	}
	c.startActivityPersistence()
	c.startGroupHealthMonitor()
	c.startRecoveryScheduler()
	c.startTunnelExpiry()
	c.startCleanupMonitor()
	c.startSandboxMonitor()
	return nil
}

// Handler exposes the authenticated remote API.
func (c *Controller) Handler() http.Handler {
	// Serve remote requests without local administrator privileges.
	return c.managementHandler(false)
}

// LocalHandler exposes privileged administration through the protected Unix listener.
func (c *Controller) LocalHandler() http.Handler {
	// Serve Unix-socket requests with local administrator privileges.
	return c.managementHandler(true)
}

// Address reads the active TCP address under the state lock.
func (c *Controller) Address() string { c.mu.Lock(); defer c.mu.Unlock(); return c.address }

// Listen creates protected listeners and starts their matching privilege boundaries.
func (c *Controller) Listen(o ListenOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Reject a second listener setup on the same controller.
	if len(c.listeners) > 0 {
		return errors.New("already listening")
	}
	// Bind TCP to loopback unless a host was explicitly configured.
	o = defaultListenerHost(o)
	// A host agent always uses its own mutual TLS listener and pinned controller identity.
	if err := c.prepareAgentListener(&o); err != nil {
		return err
	}
	tcp, err := validateListener(o)
	// Abort listener setup if its security or binding prerequisites fail.
	if err != nil {
		return err
	}
	var listeners []net.Listener
	var handlers []http.Handler
	// Close already opened listeners if a later listener fails.
	cleanup := func() { closeListeners(listeners) }
	// Abort listener setup if its security or binding prerequisites fail.
	if o.SocketPath != "" {
		l, err := protectedSocket(o.SocketPath)
		// Abort listener setup if its security or binding prerequisites fail.
		if err != nil {
			return err
		}
		listeners = append(listeners, l)
		handlers = append(handlers, c.LocalHandler())

	}
	// Create a remote listener only when TCP was selected.
	if tcp {
		l, err := net.Listen("tcp", net.JoinHostPort(o.Host, fmt.Sprint(o.Port)))
		// Abort listener setup if its security or binding prerequisites fail.
		if err != nil {
			cleanup()
			return err
		}
		c.address = l.Addr().String()
		// Wrap the listener with configured TLS before accepting remote requests.
		if o.TLSConfig != nil {
			cfg := o.TLSConfig.Clone()
			cfg.NextProtos = []string{"h2", "http/1.1"}
			l = tls.NewListener(l, cfg)
		}
		listeners = append(listeners, l)
		handlers = append(handlers, c.remoteListenerHandler(o))
	}
	c.agentOnly = o.ControllerFingerprint != ""
	pin := o.MasterFingerprint
	if c.state.MasterFingerprint != nil {
		pin = *c.state.MasterFingerprint
	}
	if pin != "" && !c.agentOnly {
		peer, err := c.newMasterPeer(pin)
		if err != nil {
			cleanup()
			return err
		}
		l, handler, err := c.openMasterListener()
		if err != nil {
			peer.cancel()
			cleanup()
			return err
		}
		c.master = masterListener{peer: peer, open: true}
		listeners = append(listeners, l)
		handlers = append(handlers, handler)
	}
	// Collector credentials never share the fleet management listener or its pinned leaf.
	listeners, handlers, err = c.appendCollectorListener(o, listeners, handlers)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		cleanup()
		return err
	}
	c.serveListeners(listeners, handlers)
	c.listeners = listeners
	c.log("Controller listeners ready", "", "info")
	c.startManagedCollector(o)
	return c.store.Save(c.state)
}

// Close stops request handling, waits for operations and durably closes controller state.
func (c *Controller) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.signalOperationChange()
	c.signalJobChange()
	c.closeTunnels()
	servers := append([]*http.Server{}, c.servers...)
	c.mu.Unlock()
	// Apply listener lifecycle handling to every prepared endpoint.
	for _, s := range servers {
		s.Shutdown(ctx)
	}
	c.cancel()
	done := make(chan struct{})
	// Signal completion only after all background operations have exited.
	go func() { c.wg.Wait(); close(done) }()
	// Wait for background completion while respecting the shutdown deadline.
	select {
	// Continue durable shutdown after all background operations finish.
	case <-done:
	// Respect the caller deadline while background work is still ending.
	case <-ctx.Done():
		return ctx.Err()
	}
	// Close diagnostic storage without holding the deployment authority lock.
	c.closeTelemetry()
	c.closeManagedCollector()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log("Controller shutdown", "", "info")
	// Require durable state persistence before reporting success or scheduling side effects.
	if err := c.store.Save(c.state); err != nil {
		c.store.Close()
		return err
	}
	return c.store.Close()
}

// maxRequestBytes includes JSON escaping for the bounded 256 KiB managed file bundle.
const maxRequestBytes = 2 << 20

// decode accepts exactly one bounded JSON object with only recognized fields.
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes+1))
	d.DisallowUnknownFields()
	// Reject this path: Invalid JSON or request fields.
	if err := d.Decode(v); err != nil {
		return model.Err("INVALID_REQUEST", "Invalid JSON or request fields")
	}
	// Reject this path: Unexpected request content.
	if err := d.Decode(new(any)); err != io.EOF {
		return model.Err("INVALID_REQUEST", "Unexpected request content")
	}
	return nil
}

// required bounds caller-supplied identifiers before storage or use.
func required(v string) error {
	// Reject this path: A nonempty bounded string is required.
	if len(v) == 0 || len(v) > 200 {
		return model.Err("INVALID_REQUEST", "A nonempty bounded string is required")
	}
	return nil
}

// log retains bounded events and writes protected execution diagnostics.
func (c *Controller) log(message, op, level string) {
	c.signalOperationChange()
	c.state.EventSequence++
	entry := LogEntry{Sequence: c.state.EventSequence, Timestamp: c.opts.Now().UTC().Format(time.RFC3339Nano), Level: level, Message: message, OperationID: op}
	if operation := c.state.Operations[op]; operation != nil {
		entry.Actor = operation.Actor
		if operation.Kind == "deploy" || operation.Kind == "action" {
			entry.Application = operation.Name
		}
	}
	c.state.Events = append(c.state.Events, entry)
	// Retain only the newest thousand public execution events.
	if len(c.state.Events) > 1000 {
		c.state.Events = c.state.Events[len(c.state.Events)-1000:]
	}
	// Write a protected execution log when a destination was configured.
	if c.opts.LogPath != "" {
		c.appendLog(entry)
	}
}

// credentials issues a bearer credential while persisting only its verifier.
func (c *Controller) credentials(name string) map[string]any {
	id, t := random(), random()
	c.state.Clients[id] = &Client{ID: id, Name: name, Verifier: verifier(t)}
	return map[string]any{"clientId": id, "token": t}
}

// valid rejects missing, consumed, cancelled or expired enrollment challenges.
func (c *Controller) valid(e *Enrollment) error {
	// Reject this path: Enrollment invalid or expired.
	if e == nil || e.Consumed || e.Cancelled || e.ExpiresAt <= c.opts.Now().UnixMilli() {
		return model.Err("INVALID_ENROLLMENT", "Enrollment invalid or expired")
	}
	return nil
}

// references pins available secret revisions for the exact deployment being reviewed.
func (c *Controller) references(spec model.Project, exact map[string]SecretRef) (map[string]SecretRef, error) {
	refs := map[string]SecretRef{}
	// Check every retained secret revision so older credentials remain protected.
	for env, name := range model.SecretBindings(spec) {
		secret := c.state.Secrets[name]
		// Reject this path: Referenced secret unavailable.
		if secret == nil {
			return nil, model.Err("SECRET_NOT_FOUND", "Referenced secret unavailable")
		}
		if secret.Owner != "" && secret.Owner != spec.Name {
			return nil, model.Err("SECRET_OWNER", "Secret belongs to another application")
		}
		rev := secret.Current
		// Use the pinned rollback revision rather than the current secret revision.
		if exact != nil {
			rev = exact[env].Revision
		}
		// Reject this path: Referenced secret revision unavailable.
		if _, ok := secret.Revisions[rev]; !ok {
			return nil, model.Err("SECRET_NOT_FOUND", "Referenced secret revision unavailable")
		}
		refs[env] = SecretRef{name, rev}
	}
	return refs, nil
}

// routeFree rejects routes that overlap active or accepted applications.
func (c *Controller) routeFree(spec model.Project) (model.Route, error) {
	// Private-only declarations do not allocate edge authority.
	if !model.Public(spec) {
		return model.Route{}, nil
	}
	primary, err := config.ParseRoute(spec.Route, c.state.Domains, c.state.Hostname)
	// The primary still supplies the backward-compatible deployment URL.
	if err != nil {
		return primary, err
	}
	// Every named public route independently reserves its host and path boundary.
	for _, project := range model.EndpointProjects(spec) {
		route, err := config.ParseRoute(project.Route, c.state.Domains, c.state.Hostname)
		// Secondary routes cannot bypass domain ownership or controller-host reservations.
		if err != nil {
			return primary, err
		}
		// Active and accepted deployments retain all their named reservations.
		if err := c.checkRouteReservation(spec.Name, route); err != nil {
			return primary, err
		}
	}
	return primary, nil
}

// checkRouteReservation compares one proposed endpoint with every retained named route.
func (c *Controller) checkRouteReservation(name string, route model.Route) error {
	// The wildcard tunnel namespace never overlaps ordinary deployment ingress.
	if c.tunnelHost(route.Hostname) {
		return model.Err("ROUTE_CONFLICT", "Hostname belongs to the tunnel namespace")
	}
	// Same-app replacements preserve the established ownership exception.
	for _, other := range c.reservedProjects() {
		// Private providers do not compete for public DNS.
		if other.Name == name {
			continue
		}
		// Secondary endpoints are as authoritative as the legacy primary route.
		for endpoint, project := range model.EndpointProjects(other) {
			r, err := config.ParseRoute(project.Route, c.state.Domains, c.state.Hostname)
			// Invalid retained route state must not be silently overwritten.
			if err != nil {
				return err
			}
			// Route identity remains segment-aware across protocols.
			if routesOverlap(r, route) {
				return model.Err("ROUTE_CONFLICT", config.RouteConflictMessage(other.Name, endpoint, r, route))
			}
		}
	}
	return nil
}

// redact removes every retained plaintext secret revision from diagnostic output.
func (c *Controller) redact(v string) string {
	// Check every retained secret revision so older credentials remain protected.
	for _, secret := range c.state.Secrets {
		// Inspect retained revisions because rollback still depends on their immutable inputs.
		for _, encrypted := range secret.Revisions {
			plain, e := c.store.Decrypt(encrypted)
			// Replace every successfully decrypted nonempty secret value in diagnostics.
			if e == nil && plain != "" {
				v = strings.ReplaceAll(v, plain, "[REDACTED]")
			}
		}
	}
	return c.redactWorkflow(v)
}

// defaultOptions supplies conservative timeouts and an injectable clock when omitted.
func defaultOptions(o Options) Options {
	o.PlanningTimeout = boundedPlanningTimeout(o.PlanningTimeout)
	// Use the system clock unless a deterministic clock was injected.
	if o.Now == nil {
		o.Now = time.Now
	}
	// Bound ordinary response writes to thirty seconds by default.
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 30 * time.Second
	}
	// Allow a longer bounded deadline for backup and export responses.
	if o.LongOperationTimeout == 0 {
		o.LongOperationTimeout = 10 * time.Minute
	}
	// Expire pairing challenges after ten minutes by default.
	if o.PairingTTL == 0 {
		o.PairingTTL = 10 * time.Minute
	}
	// Expire approval plans after ten minutes by default.
	if o.PlanTTL == 0 {
		o.PlanTTL = 10 * time.Minute
	}

	return o
}

// reconcileState migrates persisted state and applies authoritative validated server configuration.
func (c *Controller) reconcileState(conf model.ServerConfig) error {
	c.initializeWorkflows()
	// Initialize backup metadata for states written before backup support.
	if c.state.Backups == nil {
		c.state.Backups = map[string]BackupRecord{}
	}
	// Initialize resource inventory for earlier durable state versions.
	if c.state.Resources == nil {
		c.state.Resources = map[string]*ResourceRecord{}
	}
	// Initialize encrypted resource credentials for earlier durable state versions.
	if c.state.ResourceCredentials == nil {
		c.state.ResourceCredentials = map[string]string{}
	}
	c.inferResourceOwners()
	// Keep every active or accepted route authorized by the configured domains.
	if err := c.validateDomains(conf.Domains); err != nil {
		return err
	}
	// Invalidate prior plans when authoritative configured domains changed.
	if config.Fingerprint(c.state.Domains) != config.Fingerprint(conf.Domains) {
		c.state.Domains = append([]string{}, conf.Domains...)
		c.state.Generation++
	}
	return c.migrateSecretWrites()
}

// migrateSecretWrites replaces guessing-prone legacy replay signatures with keyed verifiers.
func (c *Controller) migrateSecretWrites() error {
	// Preserve replay identities while migrating their stored authentication signatures.
	for key, write := range c.state.Writes {
		// Migrate only replay signatures that lack keyed authentication.
		if !strings.HasPrefix(write.Signature, "hmac:") {
			secret := c.state.Secrets[write.Name]
			// Drop orphaned replay metadata that no longer identifies a retained secret revision.
			if secret == nil || secret.Revisions[write.Revision] == "" {
				delete(c.state.Writes, key)
				continue
			}
			plain, e := c.store.Decrypt(secret.Revisions[write.Revision])
			// Stop when encrypted protected data cannot be authenticated and decrypted.
			if e != nil {
				return e
			}
			write.Signature = c.store.Authenticate(write.Name + "|" + plain)
			c.state.Writes[key] = write
		}
	}

	return nil
}

// reservedProjects collects active and recoverable deployments whose routes remain reserved.
func (c *Controller) reservedProjects() []model.Project {
	specs := []model.Project{}
	// Include every retained application when enforcing the shared state invariant.
	for _, a := range c.state.Applications {
		// Retain active application routes as reserved public addresses.
		if a.Status != "deleted" {
			specs = append(specs, a.Spec)
		}
	}
	// Inspect accepted operations as well as active state before allowing conflicting work.
	for _, o := range c.state.Operations {
		// Retain queued, running and interrupted deployment routes until recovery or supersession.
		if o.Kind == "deploy" && (o.Status == "queued" || o.Status == "running" || o.Status == "interrupted") {
			specs = append(specs, o.Spec)
		}
	}

	return specs
}

// routesOverlap detects hostname and parent-path collisions between public routes.
func routesOverlap(a, b model.Route) bool {
	return config.RouteScopesOverlap(a, b)
}

// appendLog writes owner-only execution logs while reporting storage failures safely.
func (c *Controller) appendLog(entry LogEntry) {
	err := os.MkdirAll(filepath.Dir(c.opts.LogPath), 0700)
	// Rotate and open logs only after their protected directory is available.
	if err == nil {
		c.rotateLog()

		var f *os.File
		f, err = os.OpenFile(c.opts.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		// Set owner-only permissions before writing the opened log file.
		if err == nil {
			// Do not write a log file whose owner-only permissions could not be enforced.
			if modeErr := f.Chmod(0600); modeErr != nil {
				f.Close()
				fmt.Fprintln(os.Stderr, "Hostlier execution log permissions failed")
				return
			}
			data, _ := json.Marshal(entry)
			_, err = f.Write(append(data, '\n'))
			f.Close()
		}
	}
	// Report write failures without including potentially sensitive log content.
	if err != nil {
		fmt.Fprintln(os.Stderr, "Hostlier execution log write failed")
	}
}

// rotateLog bounds log storage by retaining a small sequence of older files.
func (c *Controller) rotateLog() {
	// Rotate only when the current log exceeds the one-megabyte limit.
	if st, e := os.Stat(c.opts.LogPath); e == nil && st.Size() > 1048576 {
		// Shift the bounded archive chain from oldest to newest before rotating the current log.
		for i := 2; i >= 0; i-- {
			from := c.opts.LogPath
			// Address numbered archives after handling the current log path.
			if i > 0 {
				from = fmt.Sprintf("%s.%d", from, i)
			}
			os.Rename(from, fmt.Sprintf("%s.%d", c.opts.LogPath, i+1))
		}

	}
}

// validateListener requires TLS whenever TCP would be exposed beyond loopback.
func validateListener(o ListenOptions) (bool, error) {
	tcp := o.TCP || o.SocketPath == ""
	// Reject this path: Nonlocal listener requires TLS.
	if tcp && o.TLSConfig == nil && o.Host != "127.0.0.1" && o.Host != "::1" && o.Host != "localhost" {
		return false, model.Err("TLS_REQUIRED", "Nonlocal listener requires TLS")
	}

	return tcp, nil
}

// protectedSocket creates an owner-only local administration socket or closes it on failure.
func protectedSocket(path string) (net.Listener, error) {
	l, err := net.Listen("unix", path)
	// Abort local administration setup when the Unix socket cannot be bound.
	if err != nil {
		return nil, err
	}
	// Enforce the intended file permissions before exposing the artifact.
	if err = os.Chmod(path, 0600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// interruptPendingOperations marks unfinished work for operator review instead of replaying side effects.
func (c *Controller) interruptPendingOperations() {
	// Inspect accepted operations as well as active state before allowing conflicting work.
	for _, op := range c.state.Operations {
		// Turn unfinished accepted work into an operator-visible recovery state.
		if op.Status == "queued" || op.Status == "running" {
			op.Status = "interrupted"
			c.log("Interrupted operation requires operator recovery", op.ID, "warn")
			op.Error = &model.Error{Code: "RECOVERY_REQUIRED", Message: "Interrupted operation requires inspection and a new approved plan"}
		}
	}
}

// serveListeners starts bounded HTTP servers for each prepared listener.
func (c *Controller) serveListeners(listeners []net.Listener, handlers []http.Handler) {
	// Apply listener lifecycle handling to every prepared endpoint.
	for i, l := range listeners {
		server := &http.Server{Handler: handlers[i], ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: c.opts.WriteTimeout, MaxHeaderBytes: 16384}
		server.Protocols = new(http.Protocols)
		server.Protocols.SetHTTP1(true)
		server.Protocols.SetHTTP2(true)
		server.Protocols.SetUnencryptedHTTP2(true)
		c.servers = append(c.servers, server)
		go server.Serve(l)
	}
}

// closeListeners releases partial listener setup when a later bind fails.
func closeListeners(listeners []net.Listener) {
	// Validate each entry before accepting the complete collection.
	for _, listener := range listeners {
		listener.Close()
	}
}
