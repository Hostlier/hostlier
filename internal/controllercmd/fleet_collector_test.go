package controllercmd

import (
	"strings"
	"testing"
)

// TestFleetListenerSettingsRequiresPin keeps ordinary controllers private while enabling pinned host agents.
func TestFleetListenerSettingsRequiresPin(t *testing.T) {
	// Missing or malformed pins cannot authorize an exposed controller socket.
	for _, pin := range []string{"", "bad", strings.Repeat("a", 64)} {
		s := session{flags: map[string]string{"host": "0.0.0.0", "port": "7463", "controller-fingerprint": pin}}
		settings, err := s.controllerSettings(t.TempDir())
		// Only the complete enrolled peer fingerprint permits nonlocal binding.
		if pin == strings.Repeat("a", 64) {
			// A valid agent must retain the explicit host and port for its mTLS listener.
			if err != nil || settings.host != "0.0.0.0" || settings.port != 7463 {
				t.Fatal(settings, err)
			}
		} else if err == nil { // Ordinary unauthenticated listeners must still be refused.
			t.Fatal("nonlocal listener accepted without a valid fleet pin")
		}
	}
}
