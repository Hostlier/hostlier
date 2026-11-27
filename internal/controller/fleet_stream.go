package controller

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

// fleetSession belongs to one authenticated member identity and one live stream.
type fleetSession struct {
	conn   *http2.ClientConn
	cancel context.CancelFunc
}

// enrolledPin is called under the controller lock.
func (c *Controller) enrolledPin(pin string) bool {
	for _, host := range c.state.Hosts {
		if host.Fingerprint == pin {
			return true
		}
	}
	return false
}

// fleetServerTLS checks enrollment on each new TLS handshake.
func (c *Controller) fleetServerTLS() (*tls.Config, error) {
	identity, err := c.fleetIdentity()
	if err != nil {
		return nil, err
	}
	return fleet.ServerTLS(identity, func(pin string) bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.closed && c.enrolledPin(pin)
	})
}

// fleetStreamHandler exposes only the reverse transport on the dedicated port.
func (c *Controller) fleetStreamHandler() http.Handler {
	handler := connect.NewBidiStreamHandler(fleet.ConnectProcedure, c.acceptFleetStream, connect.WithReadMaxBytes(fleet.FrameLimit+16))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != fleet.ConnectProcedure || r.ProtoMajor != 2 || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.Header.Get("Content-Type") != "application/grpc" {
			http.Error(w, "Authenticated fleet gRPC required", http.StatusForbidden)
			return
		}
		// Fleet streams outlive ordinary API request deadlines.
		_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), rpcTransportKey{}, rpcTransport{r, w})))
	})
}

// acceptFleetStream authenticates the reverse TLS peer before publishing a session.
func (c *Controller) acceptFleetStream(ctx context.Context, stream *connect.BidiStream[wrapperspb.BytesValue, wrapperspb.BytesValue]) error {
	wire := ctx.Value(rpcTransportKey{}).(rpcTransport)
	sum := sha256.Sum256(wire.request.TLS.PeerCertificates[0].Raw)
	pin := hex.EncodeToString(sum[:])
	c.mu.Lock()
	if c.closed || !c.enrolledPin(pin) {
		c.mu.Unlock()
		return connect.NewError(connect.CodePermissionDenied, errors.New("Controller is not enrolled"))
	}
	identity, err := c.fleetIdentity()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	cfg, err := fleet.TLS(identity, pin, false)
	if err != nil {
		return err
	}
	cfg.NextProtos = []string{"h2"}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	pipe, disconnected, wait := fleet.StreamConn(ctx, stream.Send, stream.Receive)
	conn := tls.Client(pipe, cfg)
	// Join the sender before Connect writes trailers on the same response writer.
	defer func() {
		cancel()
		pipe.Close()
		_ = http.NewResponseController(wire.writer).SetWriteDeadline(time.Now())
		wire.request.Body.Close()
		wait()
		conn.Close()
	}()
	handshake, done := context.WithTimeout(ctx, 10*time.Second)
	err = conn.HandshakeContext(handshake)
	done()
	if err != nil {
		return err
	}
	transport := &http2.Transport{ReadIdleTimeout: 30 * time.Second, PingTimeout: 10 * time.Second}
	client, err := transport.NewClientConn(conn)
	if err != nil {
		return err
	}
	defer func() { cancel(); pipe.Close(); client.Close() }()
	session := &fleetSession{client, cancel}
	c.mu.Lock()
	if c.closed || !c.enrolledPin(pin) {
		c.mu.Unlock()
		return connect.NewError(connect.CodePermissionDenied, errors.New("Controller enrollment revoked"))
	}
	if c.fleetSessions == nil {
		c.fleetSessions = make(map[string]*fleetSession)
	}
	if previous := c.fleetSessions[pin]; previous != nil {
		previous.cancel()
	}
	c.fleetSessions[pin] = session
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.fleetSessions[pin] == session {
			delete(c.fleetSessions, pin)
		}
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-disconnected:
		return connect.NewError(connect.CodeUnavailable, errors.New("Controller disconnected"))
	}

}

// fleetTransport never falls back to opening a connection to a member address.
func (c *Controller) fleetTransport(host HostRecord) (http.RoundTripper, error) {
	if session := c.fleetSessions[host.Fingerprint]; session != nil && session.conn.CanTakeNewRequest() {
		return session.conn, nil
	}
	return nil, model.Err("HOST_UNAVAILABLE", "Controller has no active outbound connection to this master; inspect its operations before retrying a mutation")
}

// connectMaster reconnects transport with bounded backoff, without replaying commands.
func (c *Controller) connectMaster(peer *masterPeer, identity fleet.Identity, endpoint string) {
	defer c.wg.Done()
	delay := time.Second
	for peer.ctx.Err() == nil {
		started := time.Now()
		c.runMasterStream(peer, identity, endpoint)
		if peer.ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-peer.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}

// runMasterStream serves delegated requests on a controller-initiated connection.
func (c *Controller) runMasterStream(peer *masterPeer, identity fleet.Identity, endpoint string) {
	cfg, err := fleet.TLS(identity, peer.pin, false)
	if err != nil {
		return
	}
	transport := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithCancel(peer.ctx)
	defer cancel()
	api := connect.NewClient[wrapperspb.BytesValue, wrapperspb.BytesValue](&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint+fleet.ConnectProcedure, connect.WithGRPC(), connect.WithReadMaxBytes(fleet.FrameLimit+16))
	stream := api.CallBidiStream(ctx)
	defer stream.CloseResponse()
	defer stream.CloseRequest()
	// Send headers before waiting for the master's reverse TLS ClientHello.
	if err := stream.Send(nil); err != nil {
		return
	}
	pipe, _, wait := fleet.StreamConn(ctx, stream.Send, stream.Receive)
	conn := tls.Server(pipe, peer.tls)
	// Stop both pumps before closing the gRPC request's sending half.
	defer func() {
		cancel()
		pipe.Close()
		stream.CloseResponse()
		wait()
		conn.Close()
	}()
	handshake, done := context.WithTimeout(ctx, 10*time.Second)
	err = conn.HandshakeContext(handshake)
	done()
	if err != nil {
		return
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.serveHostAgent(w, r.WithContext(context.WithValue(r.Context(), masterPeerKey{}, peer)), peer.pin)
	})
	server := &http2.Server{MaxConcurrentStreams: 128, ReadIdleTimeout: 30 * time.Second, PingTimeout: 10 * time.Second}
	server.ServeConn(conn, &http2.ServeConnOpts{Context: ctx, Handler: handler})
}
