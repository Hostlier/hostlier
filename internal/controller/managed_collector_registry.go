package controller

import (
	"net/http"
	"strings"
	"time"
)

// serveCollectorRegistry isolates image reads from controller management authority.
func (c *Controller) serveCollectorRegistry(w http.ResponseWriter, r *http.Request) bool {
	// Keep requests or credentials outside this scoped contract excluded.
	if r.URL.Path != "/v2" && !strings.HasPrefix(r.URL.Path, "/v2/") {
		return false
	}
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	// Stop before publishing authority or readiness when the prerequisite fails.
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "Read-only collector registry", http.StatusMethodNotAllowed)
		return true
	}
	auth := r.Clone(r.Context())
	// Handle this lifecycle state before continuing bounded reconciliation.
	if username, password, ok := r.BasicAuth(); ok && username == "collector" {
		auth.Header = r.Header.Clone()
		auth.Header.Set("Authorization", "Bearer "+password)
	}
	// Challenge untrusted pulls without granting management privileges.
	if _, err := c.telemetryAuthority(auth); err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Hostlier collector"`)
		http.Error(w, "Collector credential required", http.StatusUnauthorized)
		return true
	}
	c.managed.mu.RLock()
	defer c.managed.mu.RUnlock()
	// Stop before publishing authority or readiness when the prerequisite fails.
	if c.managed.image == nil {
		http.Error(w, "Collector image unavailable", http.StatusServiceUnavailable)
		return true
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
	c.managed.image.ServeHTTP(w, r)
	return true
}
