package controller

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"

	"hostlier/internal/fleet"
	"hostlier/internal/model"
)

type masterPeerKey struct{}
type masterPeer struct {
	pin    string
	tls    *tls.Config
	ctx    context.Context
	cancel context.CancelFunc
}
type masterListener struct {
	peer *masterPeer
	open bool
}

func (c *Controller) newMasterPeer(pin string) (*masterPeer, error) {
	if pin == "" {
		return nil, nil
	}
	opts := ListenOptions{ControllerFingerprint: pin}
	if err := c.prepareAgentListener(&opts); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(c.ctx)
	return &masterPeer{pin, opts.TLSConfig, ctx, cancel}, nil
}

func (c *Controller) currentMaster() *masterPeer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.master.peer
}

// A switch updates both TLS handshakes and HTTP requests on already-established connections.
func (c *Controller) openMasterListener() (net.Listener, http.Handler, error) {
	l, err := net.Listen("tcp", ":"+fleet.Port)
	if err != nil {
		return nil, nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		if peer := c.currentMaster(); peer != nil {
			return peer.tls, nil
		}
		return nil, model.Err("HOST_REVOKED", "No connected master")
	}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := c.currentMaster()
		if peer == nil {
			http.Error(w, "No connected master", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(peer.ctx, cancel)
		defer cancel()
		defer stop()
		c.serveHostAgent(w, r.WithContext(context.WithValue(ctx, masterPeerKey{}, peer)), peer.pin)
	})
	return tls.NewListener(l, cfg), handler, nil
}

// localMaster runs under the state lock and only through the protected administration socket.
func (c *Controller) localMaster(r *http.Request) (any, error) {
	pin, err := fleet.Fingerprint(c.state.FleetIdentity.Certificate)
	if err != nil {
		return nil, err
	}
	member := fleet.Member{Hostname: c.state.Hostname, Fingerprint: pin}
	if peer := c.master.peer; peer != nil {
		member.Master = peer.pin
	}
	if c.agentOnly {
		return nil, model.Err("INVALID_HOST", "Use the installed controller service to switch masters")
	}
	switch r.Method {
	case http.MethodGet:
		return member, nil
	case http.MethodPut:
		var change fleet.MasterChange
		if err := decode(r, &change); err != nil {
			return nil, err
		}
		if change.Previous != member.Master {
			return nil, model.Err("HOST_CHANGED", "The box changed masters during enrollment")
		}
		if change.Fingerprint == pin {
			return nil, model.Err("INVALID_HOST", "A box cannot join itself")
		}
		if change.Fingerprint != member.Master {
			if err := c.switchMaster(change.Fingerprint); err != nil {
				return nil, err
			}
			member.Master = change.Fingerprint
		}
		return member, nil
	default:
		return endpointNotFound()
	}
}

func (c *Controller) switchMaster(pin string) error {
	if c.closed {
		return model.Err("CONTROLLER_UNAVAILABLE", "Controller is shutting down")
	}
	peer, err := c.newMasterPeer(pin)
	if err != nil {
		return err
	}
	var listener net.Listener
	var handler http.Handler
	if !c.master.open && peer != nil {
		listener, handler, err = c.openMasterListener()
		if err != nil {
			peer.cancel()
			return err
		}
	}
	previous := c.state.MasterFingerprint
	c.state.MasterFingerprint = &pin
	c.state.Generation++
	if err = c.store.Save(c.state); err != nil {
		c.state.MasterFingerprint = previous
		c.state.Generation--
		if listener != nil {
			listener.Close()
		}
		if peer != nil {
			peer.cancel()
		}
		return err
	}
	if c.master.peer != nil {
		c.master.peer.cancel()
	}
	c.master.peer = peer
	if listener != nil {
		c.master.open = true
		c.serveListeners([]net.Listener{listener}, []http.Handler{handler})
		c.listeners = append(c.listeners, listener)
	}
	return nil
}
