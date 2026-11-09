package controller

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"hostlier/internal/model"
)

// prepareCollectorListener opens an isolated collector role without changing the fleet TLS identity.
func (c *Controller) prepareCollectorListener(o ListenOptions) (net.Listener, http.Handler, error) {
	// Fail closed when the transport prerequisite or credential validation fails.
	if !c.opts.ManagedCollector || o.ControllerFingerprint == "" {
		return nil, nil, nil
	}
	origin, err := c.collectorTransportOrigin(o)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return nil, nil, err
	}
	identity, err := c.loadCollectorIdentity(origin.Hostname())
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return nil, nil, err
	}
	roots := x509.NewCertPool()
	// Fail closed when the transport prerequisite or credential validation fails.
	if !roots.AppendCertsFromPEM([]byte(identity.CA)) {
		return nil, nil, errors.New("invalid collector CA")
	}
	port := origin.Port()
	// Apply this transport-specific case without changing fleet management authority.
	if port == "" {
		port = "443"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(o.Host, port))
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return nil, nil, err
	}
	c.collectorTransport.mu.Lock()
	c.collectorTransport.endpoint = origin.String()
	c.collectorTransport.identity = identity
	c.collectorTransport.mu.Unlock()
	config := &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, GetCertificate: c.collectorServingCertificate}
	return tls.NewListener(listener, config), http.HandlerFunc(c.serveCollectorTransport), nil
}

// collectorTransportOrigin binds an explicit origin or the adjacent management port.
func (c *Controller) collectorTransportOrigin(o ListenOptions) (*url.URL, error) {
	endpoint := c.opts.CollectorEndpoint
	// Apply this transport-specific case without changing fleet management authority.
	if endpoint == "" {
		host := c.opts.Hostname
		// Apply this transport-specific case without changing fleet management authority.
		if host == "" {
			host = o.Host
		}
		_, port, err := net.SplitHostPort(c.address)
		// Fail closed when the transport prerequisite or credential validation fails.
		if err != nil {
			return nil, errors.New("collector requires a TCP management listener")
		}
		number, err := strconv.Atoi(port)
		// Fail closed when the transport prerequisite or credential validation fails.
		if err != nil || number >= 65535 {
			return nil, errors.New("collector adjacent port unavailable")
		}
		endpoint = "https://" + net.JoinHostPort(host, strconv.Itoa(number+1))
	}
	origin, err := url.Parse(endpoint)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil || !validCollectorOrigin(origin) {
		return nil, errors.New("collector endpoint must be a concrete HTTPS origin")
	}
	return origin, nil
}

// validCollectorOrigin excludes wildcard destinations and routing or credential injection.
func validCollectorOrigin(origin *url.URL) bool {
	// Fail closed when the transport prerequisite or credential validation fails.
	if origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	ip := net.ParseIP(origin.Hostname())
	return ip == nil || !ip.IsUnspecified()
}

// collectorServingCertificate reads the current renewed server certificate during each TLS handshake.
func (c *Controller) collectorServingCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.collectorTransport.mu.Lock()
	defer c.collectorTransport.mu.Unlock()
	// Fail closed when the transport prerequisite or credential validation fails.
	if c.collectorTransport.identity == nil {
		return nil, errors.New("collector identity unavailable")
	}
	cert, err := c.collectorTransport.identity.collectorServerCertificate()
	return &cert, err
}

// serveCollectorTransport requires dedicated mutual TLS before allowing only ingest and image reads.
func (c *Controller) serveCollectorTransport(w http.ResponseWriter, r *http.Request) {
	// Fail closed when the transport prerequisite or credential validation fails.
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		http.Error(w, "Collector identity required", http.StatusForbidden)
		return
	}
	// Fail closed when the transport prerequisite or credential validation fails.
	if r.URL.Path == "/v2" || strings.HasPrefix(r.URL.Path, "/v2/") {
		c.serveCollectorRegistry(w, r)
		return
	}
	// Fail closed when the transport prerequisite or credential validation fails.
	if telemetryUploadRoute(r) {
		c.serveTelemetry(w, r, false)
		return
	}
	http.Error(w, "Collector route denied", http.StatusForbidden)
}

// collectorDeploymentTLS refreshes leaf credentials before runtime reconciliation and leaves the issuer stable.
func (c *Controller) collectorDeploymentTLS() (string, *model.CollectorTLS, error) {
	c.collectorTransport.mu.Lock()
	defer c.collectorTransport.mu.Unlock()
	// Fail closed when the transport prerequisite or credential validation fails.
	if c.collectorTransport.identity == nil {
		return "", nil, nil
	}
	identity, err := c.loadCollectorIdentity(c.collectorTransport.identity.Host)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return "", nil, err
	}
	c.collectorTransport.identity = identity
	return c.collectorTransport.endpoint, identity.collectorTransportMaterial(), nil
}

// defaultListenerHost retains loopback as the ordinary listener default.
func defaultListenerHost(o ListenOptions) ListenOptions {
	// Apply this transport-specific case without changing fleet management authority.
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	return o
}

// appendCollectorListener adds the isolated listener while preserving existing management endpoints.
func (c *Controller) appendCollectorListener(o ListenOptions, listeners []net.Listener, handlers []http.Handler) ([]net.Listener, []http.Handler, error) {
	listener, handler, err := c.prepareCollectorListener(o)
	// Fail closed when the transport prerequisite or credential validation fails.
	if err != nil {
		return listeners, handlers, err
	}
	// Apply this transport-specific case without changing fleet management authority.
	if listener != nil {
		listeners = append(listeners, listener)
		handlers = append(handlers, handler)
	}
	return listeners, handlers, nil
}
