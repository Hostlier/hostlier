package controller

import "testing"

// TestFleetTelemetryHistoryIsReadOnly preserves remote query filters without exposing collector administration.
func TestFleetTelemetryHistoryIsReadOnly(t *testing.T) {
	f := newHumanFixture(t)
	enrolledHostFixture(t, f, "worker")
	// History filters cross the real pinned mTLS delegation boundary.
	for _, test := range []struct {
		path   string
		status int
	}{
		{"telemetry/history?signal=logs&limit=1", 200},
		{"telemetry/history?signal=logs&limit=0", 400},
		{"telemetry/collectors", 404},
	} {
		status, result := humanHTTP(t, f.c, false, "GET", "/v1/hosts/worker/proxy/"+test.path, nil, f.token, "")
		// Invalid filters must still be rejected on the remote host and credentials stay private.
		if status != test.status {
			t.Fatalf("%s: %d %v", test.path, status, result)
		}
	}
}
