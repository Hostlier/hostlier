package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// fusedConnectDynamic relays the loopback authorization code while all OAuth credentials stay on the controller.
func (s *session) fusedConnectDynamic() error {
	return s.fusedConnectDynamicWith(map[string]any{"engineUrl": s.flags["engine-url"]})
}

// fusedConnectDynamicWith shares loopback delivery and PKCE-state validation across both consent modes.
func (s *session) fusedConnectDynamicWith(body map[string]any) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	// No authorization attempt is created unless the callback can be received.
	if err != nil {
		return err
	}
	defer listener.Close()
	callback := "http://" + listener.Addr().String() + "/fused/callback"
	path := "/v1/fused/" + url.PathEscape(s.target)
	var pending struct {
		URL             string    `json:"authorizationUrl"`
		ExpiresAt       time.Time `json:"expiresAt"`
		ClientExpiresAt time.Time `json:"clientExpiresAt"`
	}
	body["redirectUri"] = callback
	// Registration remains an owner-approved controller operation.
	if err = s.request("POST", path+"/connect", body, &pending); err != nil {
		return err
	}
	authorization, err := url.Parse(pending.URL)
	// Reject malformed controller responses before waiting for a browser callback.
	if err != nil {
		return errors.New("controller returned an invalid authorization URL")
	}
	state := authorization.Query().Get("state")
	// Every callback must be bound to an unpredictable authorization attempt.
	if state == "" {
		return errors.New("controller returned no authorization state")
	}
	ctx, cancel := context.WithDeadline(s.ctx, pending.ExpiresAt)
	defer cancel()
	result := make(chan error, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second}
	server.Handler = s.fusedRelayHandler(ctx, path, state, listener.Addr().String(), result)
	// The listener accepts only this attempt and stops when the command exits.
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	// Stop the attempt if its authorization URL cannot be shown to the user.
	if err = s.print(map[string]any{"authorizationUrl": pending.URL, "expiresAt": pending.ExpiresAt, "clientExpiresAt": pending.ClientExpiresAt}); err != nil {
		return err
	}
	// Open the interactive deploy consent page only on the configured Engine origin.
	if s.openDeployConsent(authorization, body["engineUrl"]) {
		s.openPairingBrowser(pending.URL)
	}
	// The browser callback or cancellation determines the result; no polling is needed.
	select {
	case err = <-result: // Return the controller's completed authorization result.
		return err
	case <-ctx.Done(): // End the relay when its authorization window or caller expires.
		return errors.New("OAuth connection timed out or was cancelled; reconnect to try again")
	}
}

// fusedRelayHandler accepts one matching callback and sends no OAuth tokens to the CLI or browser.
func (s *session) fusedRelayHandler(ctx context.Context, path, state, host string, result chan<- error) http.Handler {
	var once sync.Once
	// Bind the HTTP callback to the listener authority and original unpredictable state.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		// Reject callbacks for another listener, path or authorization attempt.
		if !validFusedRelay(r, host, state) {
			http.Error(w, "Invalid OAuth callback", 400)
			return
		}
		used := false
		// A replay must never issue a second token exchange.
		once.Do(func() {
			used = true
			body := map[string]string{"state": state, "code": r.URL.Query().Get("code"), "error": r.URL.Query().Get("error")}
			var connected any
			err := s.remote.Request(ctx, "POST", path+"/complete", body, &connected)
			// Relay only the resulting status; Engine credentials stay on the controller.
			if err != nil {
				http.Error(w, "Authorization failed. Return to Hostlier.", 400)
			} else {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Write([]byte("Connected to Fused. Return to Hostlier to deploy your Minion app.\n"))
			}
			http.NewResponseController(w).Flush()
			result <- err
		})
		// Report replayed callbacks without repeating the controller token exchange.
		if !used {
			http.Error(w, "Authorization already used", 409)
		}
	})
}

// validFusedRelay rejects alternate hosts, paths, methods and unrelated browser callbacks.
func validFusedRelay(r *http.Request, host, state string) bool {
	return r.Method == "GET" && r.Host == host && r.URL.Path == "/fused/callback" && r.URL.Query().Get("state") == state && len(r.URL.RawQuery) < 8192
}

// openDeployConsent limits browser launches to the Engine explicitly configured by this interactive deployment.
func (s *session) openDeployConsent(u *url.URL, engine any) bool {
	return s.command == "deploy" && s.interactive && u.Scheme+"://"+u.Host == engine
}
