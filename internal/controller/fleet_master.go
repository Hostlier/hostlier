package controller

import (
	"context"
	"crypto/tls"
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

// localMaster runs under the state lock and only through the protected administration socket.
func (c *Controller) localMaster(r *http.Request) (any, error) {
	pin, err := fleet.Fingerprint(c.state.FleetIdentity.Certificate)
	if err != nil {
		return nil, err
	}
	member := fleet.Member{Hostname: c.state.Hostname, Fingerprint: pin}
	if peer := c.master.peer; peer != nil {
		member.Master = peer.pin
		member.MasterURL = c.state.MasterURL
	}
	if c.agentOnly {
		return nil, model.Err("INVALID_HOST", "Use the installed controller service to switch masters")
	}
	switch r.Method {
	case http.MethodGet:
		return member, nil
	case http.MethodPut:
		if c.controlPlaneOnly() {
			return nil, model.Err("CONTROLLER_MODE", "A control-plane controller cannot join as a workload worker")
		}
		var change fleet.MasterChange
		if err := decode(r, &change); err != nil {
			return nil, err
		}
		if change.Previous != member.Master || change.PreviousURL != nil && *change.PreviousURL != member.MasterURL {
			return nil, model.Err("HOST_CHANGED", "The box changed masters during enrollment")
		}
		if change.Fingerprint == pin {
			return nil, model.Err("INVALID_HOST", "A box cannot join itself")
		}
		if change.Fingerprint != member.Master || change.URL != member.MasterURL {
			if err := c.switchMaster(change.Fingerprint, change.URL); err != nil {
				return nil, err
			}
			member.Master = change.Fingerprint
			member.MasterURL = change.URL
		}
		return member, nil
	default:
		return endpointNotFound()
	}
}

func (c *Controller) switchMaster(pin, endpoint string) error {
	if pin != "" && endpoint != "" && !validHostURL(endpoint) {
		return model.Err("INVALID_HOST", "Master URL must be an HTTPS origin for its fleet gRPC endpoint")
	}
	if pin == "" && endpoint != "" {
		return model.Err("INVALID_HOST", "Master URL requires a fingerprint")
	}
	if c.closed {
		return model.Err("CONTROLLER_UNAVAILABLE", "Controller is shutting down")
	}
	peer, err := c.newMasterPeer(pin)
	if err != nil {
		return err
	}
	identity, err := c.fleetIdentity()
	if err != nil {
		if peer != nil {
			peer.cancel()
		}
		return err
	}
	previous, previousURL := c.state.MasterFingerprint, c.state.MasterURL
	c.state.MasterURL = endpoint
	c.state.MasterFingerprint = &pin
	c.state.Generation++
	if err = c.store.Save(c.state); err != nil {
		c.state.MasterFingerprint = previous
		c.state.MasterURL = previousURL
		c.state.Generation--
		if peer != nil {
			peer.cancel()
		}
		return err
	}
	if c.master.peer != nil {
		c.master.peer.cancel()
	}
	c.master.peer = peer
	if peer != nil && endpoint != "" {
		c.wg.Add(1)
		go c.connectMaster(peer, identity, endpoint)
	}
	return nil
}
