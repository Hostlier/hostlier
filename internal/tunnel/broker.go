// Package tunnel forwards HTTP over authenticated outbound WebSocket connections.
package tunnel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Broker owns the bounded reverse connections for exactly one approved tunnel lease.
type Broker struct {
	ctx    context.Context
	cancel context.CancelFunc
	slots  chan struct{}
	idle   chan *pipe
	mu     sync.Mutex
	pipes  map[*pipe]bool
	proxy  *httputil.ReverseProxy
}

// NewBroker creates a lease-local HTTP handler without opening an inbound listener.
func NewBroker(limit int) (*Broker, error) {
	// Bound concurrent reverse connections independently of request body size.
	if limit < 1 || limit > 16 {
		return nil, errors.New("tunnel connection limit must be between one and sixteen")
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Broker{ctx: ctx, cancel: cancel, slots: make(chan struct{}, limit), idle: make(chan *pipe, limit), pipes: map[*pipe]bool{}}
	transport := &http.Transport{DialContext: b.dial, MaxConnsPerHost: limit, MaxIdleConnsPerHost: limit, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 65536, IdleConnTimeout: 60 * time.Second}
	b.proxy = &httputil.ReverseProxy{Transport: transport, FlushInterval: -1, Rewrite: rewrite, ErrorHandler: unavailable}
	return b, nil
}

// rewrite preserves the approved public host while selecting the lease's private transport.
func rewrite(r *httputil.ProxyRequest) {
	r.SetURL(&url.URL{Scheme: "http", Host: "tunnel.invalid"})
	r.Out.Host = r.In.Host
	r.SetXForwarded()
	// Public lease origins are HTTPS even when the managed edge terminates TLS.
	r.Out.Header.Set("X-Forwarded-Proto", "https")
}

// unavailable fails closed without exposing local app or transport diagnostics.
func unavailable(w http.ResponseWriter, _ *http.Request, _ error) {
	http.Error(w, "Tunnel unavailable", http.StatusServiceUnavailable)
}

// ServeHTTP streams public HTTP and upgrades through this broker's isolated reverse transport.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A revoked lease cannot acquire or continue serving any connection.
	if b.ctx.Err() != nil {
		unavailable(w, r, nil)
		return
	}
	// Approved HTTP streams use lease expiry rather than ordinary REST body deadlines.
	http.NewResponseController(w).SetReadDeadline(time.Time{})
	http.NewResponseController(w).SetWriteDeadline(time.Time{})
	b.proxy.ServeHTTP(w, r)
}

// AcceptHTTP upgrades a connection after the controller verifies its scoped lease token.
func (b *Broker) AcceptHTTP(w http.ResponseWriter, r *http.Request) {
	// A revoked lease cannot acquire or continue serving any connection.
	if b.ctx.Err() != nil {
		http.Error(w, "Tunnel expired", http.StatusGone)
		return
	}
	// Wait only for transport capacity, completion or explicit cancellation.
	select {
	// A revoked lease cannot acquire or continue serving any connection.
	case <-b.ctx.Done():
		http.Error(w, "Tunnel expired", http.StatusGone)
		return
	// Reserve one slot before accepting an authenticated outbound pipe.
	case b.slots <- struct{}{}:
	// Fail promptly when no live capacity or terminal notification slot is available.
	default:
		http.Error(w, "Tunnel connection capacity reached", http.StatusTooManyRequests)
		return
	}
	// Authenticated upgrades outlive ordinary REST write deadlines.
	http.NewResponseController(w).SetReadDeadline(time.Time{})
	http.NewResponseController(w).SetWriteDeadline(time.Time{})
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hostlier-http-v1"}})
	// Close failed transport work without exposing private connection diagnostics.
	if err != nil {
		<-b.slots
		return
	}
	// Require the versioned binary HTTP tunnel protocol on both peers.
	if connection.Subprotocol() != "hostlier-http-v1" {
		connection.CloseNow()
		<-b.slots
		return
	}
	p := newPipe(b.ctx, connection, b.remove)
	b.mu.Lock()
	// Revocation may race the WebSocket handshake; never install a pipe after Close.
	if b.ctx.Err() != nil {
		b.mu.Unlock()
		p.Close()
		return
	}
	b.pipes[p] = true
	b.mu.Unlock()
	go p.prefetch()
	// Close exactly this lease pipe after a failed WebSocket heartbeat.
	go heartbeat(b.ctx, p.done, p.ws, func() { p.Close() })
	// Wait only for transport capacity, completion or explicit cancellation.
	select {
	// Publish the authenticated pipe only inside its own lease.
	case b.idle <- p:
	// A lost connection must not leave its HTTP handler blocked behind stale idle entries.
	case <-p.done:
		return
	// A revoked lease cannot acquire or continue serving any connection.
	case <-b.ctx.Done():
		p.Close()
	}
	<-p.done
}

// dial selects only a live outbound connection belonging to this approved lease.
func (b *Broker) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	// Maintain bounded lease-local connections until cancelled or disconnected.
	for {
		// Wait only for transport capacity, completion or explicit cancellation.
		select {
		// Cancellation stops transport work and releases its bounded resources.
		case <-ctx.Done():
			return nil, ctx.Err()
		// A revoked lease cannot acquire or continue serving any connection.
		case <-b.ctx.Done():
			return nil, errors.New("tunnel expired")
		// Skip stale disconnected pipes rather than forwarding a request into them.
		case p := <-b.idle:
			// Keep this operation within its explicit TLS and lease ownership boundary.
			if !p.closed.Load() {
				return p, nil
			}
		// Fail promptly when no live capacity or terminal notification slot is available.
		default:
			return nil, errors.New("no connected tunnel client")
		}
	}
}

// remove releases capacity as soon as a reverse connection closes, including idle disconnects.
func (b *Broker) remove(p *pipe) {
	b.mu.Lock()
	delete(b.pipes, p)
	b.mu.Unlock()
	<-b.slots
}

// Close immediately revokes all streams and prevents reconnection to this broker.
func (b *Broker) Close() error {
	b.cancel()
	b.mu.Lock()
	connections := make([]*pipe, 0, len(b.pipes))
	// Maintain bounded lease-local connections until cancelled or disconnected.
	for p := range b.pipes {
		connections = append(connections, p)
	}
	b.mu.Unlock()
	// Maintain bounded lease-local connections until cancelled or disconnected.
	for _, p := range connections {
		p.Close()
	}
	// Require an explicit valid TCP port for the selected local application.
	if transport, ok := b.proxy.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

// firstRead retains one prefetched byte while detecting disconnected idle reverse connections.
type firstRead struct {
	value byte
	n     int
	err   error
}

// pipe adapts standard WebSocket framing to a monitored HTTP transport connection.
type pipe struct {
	net.Conn
	ws         *websocket.Conn
	first      chan firstRead
	readOnce   sync.Once
	firstValue firstRead
	done       chan struct{}
	closed     atomic.Bool
	closeOnce  sync.Once
	onClose    func(*pipe)
}

// newPipe monitors idle disconnects without consuming or buffering HTTP request bodies.
func newPipe(ctx context.Context, ws *websocket.Conn, onClose func(*pipe)) *pipe {
	p := &pipe{Conn: websocket.NetConn(ctx, ws, websocket.MessageBinary), ws: ws, first: make(chan firstRead, 1), done: make(chan struct{}), onClose: onClose}
	return p
}

// prefetch waits for one response byte or disconnect while the connection is idle.
func (p *pipe) prefetch() {
	var data [1]byte
	n, err := p.Conn.Read(data[:])
	p.first <- firstRead{value: data[0], n: n, err: err}
	// Close failed transport work without exposing private connection diagnostics.
	if err != nil {
		p.Close()
	}
}

// Read returns the monitored first byte before resuming ordinary streaming reads.
func (p *pipe) Read(data []byte) (int, error) {
	// Return the monitored prefix exactly once before resuming unbuffered streaming.
	if len(data) == 0 {
		return 0, nil
	}
	// Return the monitored prefix exactly once before resuming unbuffered streaming.
	p.readOnce.Do(func() { p.firstValue = <-p.first })
	// Return the monitored prefix exactly once before resuming unbuffered streaming.
	if p.firstValue.n > 0 {
		data[0] = p.firstValue.value
		p.firstValue.n = 0
		return 1, nil
	}
	// Return the monitored prefix exactly once before resuming unbuffered streaming.
	if p.firstValue.err != nil {
		return 0, p.firstValue.err
	}
	return p.Conn.Read(data)
}

// Close aborts active traffic and notifies the lease owner exactly once.
func (p *pipe) Close() error {
	// Revoke this pipe and release its lease capacity exactly once.
	p.closeOnce.Do(func() { p.closed.Store(true); p.ws.CloseNow(); close(p.done); p.onClose(p) })
	return nil
}

// Connected reports reverse pipes for health without exposing transport or credential details.
func (b *Broker) Connected() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pipes)
}
