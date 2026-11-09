package controller

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTelemetryCredentialIsolation verifies collector authority never becomes management authority.
func TestTelemetryCredentialIsolation(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	status, created := call(t, c, true, "POST", "/v1/telemetry/collectors", map[string]any{"name": "node-one"}, "")
	// Only successful local issuance can establish the credential tested below.
	if status != 200 {
		t.Fatalf("create %d %v", status, created)
	}
	token := created["token"].(string)
	id := created["collector"].(map[string]any)["id"].(string)
	// Collector tokens remain syntactically distinct from management credentials.
	if !strings.HasPrefix(token, "hlt_") {
		t.Fatal("missing scoped prefix")
	}
	verifyTelemetryCredentialIsolation(t, c, token)
	request := httptest.NewRequest("POST", "/v1/telemetry/otlp/logs", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	// A newly created token authorizes ingestion before revocation.
	if _, err := c.telemetryAuthority(request); err != nil {
		t.Fatal(err)
	}
	status, _ = call(t, c, true, "POST", "/v1/telemetry/collectors/"+id+"/revoke", map[string]any{}, "")
	// Explicit revocation must persist before the next request is checked.
	if status != 200 {
		t.Fatal("revoke failed", status)
	}
	// A previously usable collector token immediately loses authority for new requests.
	if _, err := c.telemetryAuthority(request); err == nil {
		t.Fatal("revoked upload accepted")
	}
}

// verifyTelemetryCredentialIsolation checks management boundaries and protected storage representation.
func verifyTelemetryCredentialIsolation(t *testing.T, c *Controller, token string) {
	t.Helper()
	// Ingestion authority must grant neither management reads nor sensitive history access.
	for _, path := range []string{"/v1/apps", "/v1/status/snapshot", "/v1/telemetry/history?signal=logs", "/v1/telemetry/collectors"} {
		status, _ := call(t, c, false, "GET", path, nil, token)
		// The same collector token must be rejected at each management-facing endpoint.
		if status != 401 {
			t.Fatalf("collector accessed %s: %d", path, status)
		}
	}
	raw, _ := json.Marshal(c.state)
	// Durable state retains a verifier rather than a reusable bearer credential.
	if strings.Contains(string(raw), token) {
		t.Fatal("state retains raw credential")
	}
	status, out := call(t, c, true, "GET", "/v1/telemetry/collectors", nil, "")
	raw, _ = json.Marshal(out)
	// Administrative listing must not disclose even the stored verifier.
	if status != 200 || strings.Contains(string(raw), "verifier") || strings.Contains(string(raw), token) {
		t.Fatal("credential exposed by list")
	}
}

// TestTelemetryCredentialExpiry verifies the fixed lifetime and runtime binding.
func TestTelemetryCredentialExpiry(t *testing.T) {
	now := time.Now()
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, func() time.Time { return now }) // Advance only the credential clock.
	defer c.Close(context.Background())
	_, created := call(t, c, true, "POST", "/v1/telemetry/collectors", map[string]any{"name": "expiry"}, "")
	request := httptest.NewRequest("POST", "/v1/telemetry/otlp/metrics", nil)
	request.Header.Set("Authorization", "Bearer "+created["token"].(string))
	now = now.Add(31 * 24 * time.Hour)
	// Crossing the fixed lifetime removes authority without an explicit revocation call.
	if _, err := c.telemetryAuthority(request); err == nil {
		t.Fatal("expired token accepted")
	}
}

// TestTelemetryCredentialFailureRollsBack prevents accepting a token that was not durably created.
func TestTelemetryCredentialFailureRollsBack(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	c.store.Close()
	status, _ := call(t, c, true, "POST", "/v1/telemetry/collectors", map[string]any{"name": "failure"}, "")
	// A failed durable write cannot leave an accepted in-memory credential.
	if status == 200 || len(c.state.TelemetryCollectors) != 0 {
		t.Fatal("failed credential persisted in memory")
	}
}

// TestTelemetryStorageIsLazy keeps a controller without collectors free of history database overhead.
func TestTelemetryStorageIsLazy(t *testing.T) {
	directory := t.TempDir()
	c := newTestController(t, directory, &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	// Controller construction alone must create no diagnostic history database.
	if _, err := os.Stat(filepath.Join(directory, "observations", "history")); !os.IsNotExist(err) {
		t.Fatal("history opened without demand")
	}
}

// TestTelemetryCredentialsRotateBeyondCapacity admits repeated rotation while retaining bounded live authority.
func TestTelemetryCredentialsRotateBeyondCapacity(t *testing.T) {
	// Both explicit revocation and natural expiry release capacity for the next credential.
	for _, mode := range []string{"revoked", "expired"} {
		t.Run(mode, func(t *testing.T) { verifyTelemetryRotation(t, mode) }) // Each lifetime policy has an independent durable controller.
	}
}

// verifyTelemetryRotation rotates more than the active credential limit through the actual API.
func verifyTelemetryRotation(t *testing.T, mode string) {
	t.Helper()
	now := time.Now()
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, func() time.Time { return now }) // Inject deterministic credential expiry.
	defer c.Close(context.Background())
	previous, token := "", ""
	// Seventy issuances exceed the 64-credential limit without growing live authority.
	for i := 0; i < 70; i++ {
		if mode == "expired" { // Expiration advances before each replacement is requested.
			now = now.Add(31 * 24 * time.Hour)
		} else if previous != "" { // Revocation applies to the previous token only.
			status, _ := call(t, c, true, "POST", "/v1/telemetry/collectors/"+previous+"/revoke", map[string]any{}, "")
			if status != 200 { // A failed revocation must not be mistaken for successful rotation.
				t.Fatal("revoke", status)
			}
		}
		status, created := call(t, c, true, "POST", "/v1/telemetry/collectors", map[string]any{"name": "rotating"}, "")
		// Pruning keeps exactly the current active credential after every successful issuance.
		if status != 200 || len(c.state.TelemetryCollectors) != 1 {
			t.Fatalf("rotation %d: status=%d retained=%d", i, status, len(c.state.TelemetryCollectors))
		}
		previous = created["collector"].(map[string]any)["id"].(string)
		token = created["token"].(string)
	}
	request := httptest.NewRequest("POST", "/v1/telemetry/otlp/metrics", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	// The final replacement remains usable after all earlier credentials are pruned.
	if _, err := c.telemetryAuthority(request); err != nil {
		t.Fatal(err)
	}
}

// TestTelemetryCredentialPruneFailureRestoresOriginalState verifies pruning and creation roll back together.
func TestTelemetryCredentialPruneFailureRestoresOriginalState(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	original := map[string]TelemetryCollector{
		"revoked": {ID: "revoked", Runtime: c.state.Runtime, Verifier: "old-revoked", Revoked: true, ExpiresAt: time.Now().Add(time.Hour)},
		"expired": {ID: "expired", Runtime: c.state.Runtime, Verifier: "old-expired", ExpiresAt: time.Now().Add(-time.Hour)},
		"live":    {ID: "live", Runtime: c.state.Runtime, Verifier: "old-live", ExpiresAt: time.Now().Add(time.Hour)},
	}
	c.state.TelemetryCollectors = original
	before, _ := json.Marshal(original)
	c.store.Close()
	status, _ := call(t, c, true, "POST", "/v1/telemetry/collectors", map[string]any{"name": "must-fail"}, "")
	after, _ := json.Marshal(c.state.TelemetryCollectors)
	// A failed durable save cannot drop existing verifiers or leak a new in-memory authority.
	if status == 200 || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed prune mutated state: status=%d before=%s after=%s", status, before, after)
	}
}

// TestTelemetryCredentialActiveCapacity preserves the bound when every retained credential remains live.
func TestTelemetryCredentialActiveCapacity(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer c.Close(context.Background())
	c.state.TelemetryCollectors = map[string]TelemetryCollector{}
	// Fill the live inventory without attributing the test to inactive cleanup behavior.
	for i := 0; i < 64; i++ {
		id := strconv.Itoa(i)
		c.state.TelemetryCollectors[id] = TelemetryCollector{ID: id, Runtime: c.state.Runtime, ExpiresAt: time.Now().Add(time.Hour)}
	}
	status, _ := call(t, c, true, "POST", "/v1/telemetry/collectors", map[string]any{"name": "overflow"}, "")
	// The next issuance must fail without changing current credential records.
	if status != 429 || len(c.state.TelemetryCollectors) != 64 {
		t.Fatal("live credential capacity exceeded", status)
	}
}
