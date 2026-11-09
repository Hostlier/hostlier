package controller

import (
	"hostlier/internal/model"
	"net/http"
	"strings"
	"time"
)

// publicAccessEndpoint allows only proof-checked ceremonies and bounded request creation.
func publicAccessEndpoint(r *http.Request) bool {
	return r.Method == "POST" && map[string]bool{"/v1/access/pair/start": true, "/v1/access/pair/redeem": true, "/v1/access/pair/poll": true, "/v1/access/passkey/register/begin": true, "/v1/access/passkey/register/finish": true, "/v1/access/passkey/decision/begin": true, "/v1/access/passkey/decision/finish": true, "/v1/access/passkey/pair/begin": true, "/v1/access/passkey/pair/finish": true}[r.URL.Path] || r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/access/pair/review/")
}

// apiAccess routes browser and terminal enrollment without granting ordinary API tokens owner authority.
func (c *Controller) apiAccess(r *http.Request, p []string, actor string) (any, error) {
	// Proceed only for this explicitly validated protocol state.
	if r.Method != "POST" {
		return c.accessRead(r, p)
	}
	// First-owner setup is available only through protected administrator transport.
	if r.URL.Path == "/v1/access/setup" {
		return c.setupCLIFromAdministrator(r, actor)
	}
	// Proceed only for this explicitly validated protocol state.
	if r.URL.Path == "/v1/access/bootstrap" {
		return c.passkeyBootstrap(r, actor)
	}
	// Proceed only for this explicitly validated protocol state.
	if r.URL.Path == "/v1/access/pair/code" {
		// Reject the request: Pairing codes require protected server administration or a passkey.
		if actor != "local" {
			return nil, model.Err("UNAUTHORIZED", "Pairing codes require protected server administration or a passkey")
		}
		return c.adminPairCode(r)
	}
	handlers := map[string]func(*http.Request) (any, error){"/v1/access/pair/start": c.startCLIPairing, "/v1/access/pair/redeem": c.redeemCLIPairing, "/v1/access/pair/poll": c.pollCLIPairing, "/v1/access/passkey/register/begin": c.beginPasskeyRegistration, "/v1/access/passkey/register/finish": c.finishPasskeyRegistration, "/v1/access/passkey/decision/begin": c.beginPasskeyDecision, "/v1/access/passkey/decision/finish": c.finishPasskeyDecision, "/v1/access/passkey/pair/begin": c.beginPasskeyPairing, "/v1/access/passkey/pair/finish": c.finishPasskeyPairing}
	// Proceed only for this explicitly validated protocol state.
	if handler := handlers[r.URL.Path]; handler != nil {
		return handler(r)
	}
	return endpointNotFound()
}

// browserOrigin rejects cross-site browser requests before allocating or consuming ceremonies.
func (c *Controller) browserOrigin(r *http.Request) error {
	// Reject the request: Use the controller's own HTTPS page.
	if r.Header.Get("Origin") != "https://"+c.state.Hostname || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return model.Err("UNAUTHORIZED", "Use the controller's own HTTPS page")
	}
	return nil
}

// pruneAccess limits unauthenticated memory retention and invalidates expired bootstrap URLs.
func (c *Controller) pruneAccess() {
	now := c.opts.Now()
	// Check every retained entry before accepting the complete set.
	for id, v := range c.access.Ceremonies {
		// Discard expired or consumed ephemeral authority.
		if !now.Before(v.ExpiresAt) {
			delete(c.access.Ceremonies, id)
		}
	}
	// Check every retained entry before accepting the complete set.
	for id, v := range c.access.Bootstraps {
		// Discard expired or consumed ephemeral authority.
		if !now.Before(v) {
			delete(c.access.Bootstraps, id)
		}
	}
	// Check every retained entry before accepting the complete set.
	for id, v := range c.access.Pairings {
		// Discard expired or consumed ephemeral authority.
		if !now.Before(v.ExpiresAt) {
			delete(c.access.Pairings, id)
		}
	}
}

// keepBrowserCeremony returns an opaque handle while retaining all authority server-side.
func (c *Controller) keepBrowserCeremony(v browserCeremony, options any) (any, error) {
	c.pruneAccess()
	// Reject the request: Too many active passkey requests.
	if len(c.access.Ceremonies) >= 100 {
		return nil, model.Err("RATE_LIMITED", "Too many active passkey requests")
	}
	id := random()
	v.ExpiresAt = c.opts.Now().Add(2 * time.Minute)
	c.access.Ceremonies[id] = v
	return map[string]any{"ceremony": id, "options": options}, nil
}

// takeBrowserCeremony consumes even failed attempts and binds each proof to its original purpose.
func (c *Controller) takeBrowserCeremony(r *http.Request, kind string) (browserCeremony, error) {
	// Require the configured controller origin before consuming any challenge.
	if err := c.browserOrigin(r); err != nil {
		return browserCeremony{}, err
	}
	id := r.Header.Get("X-Hostlier-Ceremony")
	v, ok := c.access.Ceremonies[id]
	delete(c.access.Ceremonies, id)
	// Reject the request: Passkey request expired or already consumed.
	if !ok || v.Kind != kind || !c.opts.Now().Before(v.ExpiresAt) {
		return v, model.Err("HUMAN_CEREMONY", "Passkey request expired or already consumed")
	}
	return v, nil
}

// accessRead exposes only the nonsecret review for an opaque pairing request.
func (c *Controller) accessRead(r *http.Request, p []string) (any, error) {
	// No other browser API resource is available without a verified proof.
	if r.Method == "GET" && len(p) == 5 && p[2] == "pair" && p[3] == "review" {
		return c.pairReview(p[4])
	}
	return endpointNotFound()
}
