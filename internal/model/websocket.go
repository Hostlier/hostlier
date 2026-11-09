package model

import "slices"

// ExtendedWebSocketVersions reports explicitly requested RFC 8441 and RFC 9220 ingress.
func ExtendedWebSocketVersions(e *Endpoint) (h2, h3 bool) {
	// Legacy and ordinary HTTP endpoints need no extended CONNECT capability.
	if e == nil || e.HTTP == nil {
		return false, false
	}
	// Version permissions are attached to exact WebSocket resources.
	for _, rule := range e.HTTP.Routes {
		// Other application protocols do not enable CONNECT tunnelling.
		if rule.Protocol == "websocket" {
			h2 = h2 || slices.Contains(rule.HTTPVersions, "h2")
			h3 = h3 || slices.Contains(rule.HTTPVersions, "h3")
		}
	}
	return h2, h3
}
