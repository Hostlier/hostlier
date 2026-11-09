package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Client forwards one approved local HTTP target over verified outbound TLS connections.
type Client struct {
	URL         string
	Token       string
	Target      string
	HTTPClient  *http.Client
	Connections int
}

// LocalTarget resolves literal loopback destinations without permitting remote address discovery.
func LocalTarget(value string) (string, error) {
	// Keep this operation within its explicit TLS and lease ownership boundary.
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	u, err := url.Parse(value)
	// Reject unsupported origins before opening any network connection.
	if err != nil || !localOrigin(u) {
		return "", errors.New("target must be a loopback HTTP origin with an explicit port")
	}
	host := u.Hostname()
	// Normalize localhost without trusting mutable DNS resolution.
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	// Permit only literal loopback destinations, never DNS-resolved remote addresses.
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New("target must use localhost or a literal loopback address")
	}
	port, err := strconv.Atoi(u.Port())
	// Require an explicit valid TCP port for the selected local application.
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("target requires a valid explicit port")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

// Run maintains a bounded pool and reconnects lost transports without replaying local requests.
func (c Client) Run(ctx context.Context) error {
	target, err := LocalTarget(c.Target)
	// Close failed transport work without exposing private connection diagnostics.
	if err != nil {
		return err
	}
	// Close failed transport work without exposing private connection diagnostics.
	if err = c.validate(); err != nil {
		return err
	}
	count := c.Connections
	// Bound concurrent reverse connections independently of request body size.
	if count == 0 {
		count = 4
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	fatal := make(chan error, 1)
	var workers sync.WaitGroup
	// Maintain bounded lease-local connections until cancelled or disconnected.
	for i := 0; i < count; i++ {
		workers.Add(1)
		// Keep this operation within its explicit TLS and lease ownership boundary.
		go func() { defer workers.Done(); c.worker(lifetime, target, fatal) }()
	}
	defer workers.Wait()
	// Wait only for transport capacity, completion or explicit cancellation.
	select {
	// Cancellation stops transport work and releases its bounded resources.
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	// A permanent lease rejection stops every worker.
	case err := <-fatal:
		cancel()
		return err
	}
}

// validate rejects plaintext remote transport, redirectable credentials and unbounded worker counts.
func (c Client) validate() error {
	// Reject unsupported origins before opening any network connection.
	if !secureEndpoint(c.URL) {
		return errors.New("tunnel endpoint must use verified HTTPS or WSS")
	}
	// Require a bounded header-safe scoped lease credential.
	if c.Token == "" || len(c.Token) > 512 || strings.ContainsAny(c.Token, "\r\n") {
		return errors.New("tunnel lease token is required")
	}
	// Bound concurrent reverse connections independently of request body size.
	if c.Connections < 0 || c.Connections > 16 {
		return errors.New("tunnel connection limit exceeds sixteen")
	}
	return nil
}

// worker replenishes one connection with bounded backoff after genuine transport loss.
func (c Client) worker(ctx context.Context, target string, fatal chan<- error) {
	delay := 100 * time.Millisecond
	// Maintain bounded lease-local connections until cancelled or disconnected.
	for ctx.Err() == nil {
		permanent := c.connect(ctx, target)
		// Revoked or inaccessible leases must stop instead of reconnecting forever.
		if permanent {
			// Wait only for transport capacity, completion or explicit cancellation.
			select {
			// Publish a single terminal lease error without blocking sibling workers.
			case fatal <- errors.New("tunnel lease rejected"):
			// Fail promptly when no live capacity or terminal notification slot is available.
			default:
			}
			return
		}
		// Keep this operation within its explicit TLS and lease ownership boundary.
		if !backoff(ctx, delay) {
			return
		}
		delay = min(delay*2, 2*time.Second)
	}
}

// connect authenticates one TLS stream and relays bytes only to the fixed loopback destination.
func (c Client) connect(ctx context.Context, target string) bool {
	httpClient := http.DefaultClient
	// Reuse the caller's verified controller transport and trust configuration.
	if c.HTTPClient != nil {
		httpClient = c.HTTPClient
	}
	trusted := *httpClient
	// Close failed transport work without exposing private connection diagnostics.
	trusted.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	handshake, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, response, err := websocket.Dial(handshake, c.URL, &websocket.DialOptions{HTTPClient: &trusted, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.Token}}, Subprotocols: []string{"hostlier-http-v1"}})
	cancel()
	// Close failed transport work without exposing private connection diagnostics.
	if err != nil {
		return permanentResponse(response)
	}
	defer ws.CloseNow()
	// Require the versioned binary HTTP tunnel protocol on both peers.
	if ws.Subprotocol() != "hostlier-http-v1" {
		return true
	}
	local, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", target)
	// Close failed transport work without exposing private connection diagnostics.
	if err != nil {
		return false
	}
	defer local.Close()
	connectionCtx, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan struct{})
	defer close(finished)
	// A failed heartbeat releases the local relay and reconnects through its worker.
	go heartbeat(connectionCtx, finished, ws, func() { ws.CloseNow() })
	remote := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	relay(ctx, local, remote)
	return false
}

// permanentResponse prevents retrying revoked, expired or inaccessible lease identities.
func permanentResponse(response *http.Response) bool {
	// A transport failure has no permanent HTTP lease rejection to classify.
	if response == nil {
		return false
	}
	return response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 404 || response.StatusCode == 410
}

// relay streams in both directions with fixed copy buffers and closes both sides on disconnect.
func relay(ctx context.Context, local, remote net.Conn) {
	done := make(chan struct{}, 2)
	var copies sync.WaitGroup
	copies.Add(2)
	// Keep this operation within its explicit TLS and lease ownership boundary.
	go func() { defer copies.Done(); io.Copy(local, remote); done <- struct{}{} }()
	// Keep this operation within its explicit TLS and lease ownership boundary.
	go func() { defer copies.Done(); io.Copy(remote, local); done <- struct{}{} }()
	// Wait only for transport capacity, completion or explicit cancellation.
	select {
	// Cancellation stops transport work and releases its bounded resources.
	case <-ctx.Done():
	// Either stream ending closes both sides without replaying request bytes.
	case <-done:
	}
	local.Close()
	remote.Close()
	copies.Wait()
}

// backoff bounds reconnect frequency while allowing immediate invocation cancellation.
func backoff(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	// Wait only for transport capacity, completion or explicit cancellation.
	select {
	// Cancellation stops transport work and releases its bounded resources.
	case <-ctx.Done():
		return false
	// Retry only after the bounded reconnect delay.
	case <-timer.C:
		return true
	}
}

// localOrigin excludes credentials, paths and alternate protocols from loopback targets.
func localOrigin(u *url.URL) bool {
	return u.Scheme == "http" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

// secureEndpoint restricts lease credentials to an explicit TLS origin and endpoint path.
func secureEndpoint(value string) bool {
	u, err := url.Parse(value)
	// Close failed transport work without exposing private connection diagnostics.
	if err != nil {
		return false
	}
	return (u.Scheme == "wss" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
