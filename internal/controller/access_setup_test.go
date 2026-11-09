package controller

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"hostlier/internal/model"
	"net/url"
	"testing"
	"time"
)

// setupPendingDevice creates the actual HTTPS device request without issuing any credential.
func setupPendingDevice(t *testing.T, c *Controller) map[string]any {
	t.Helper()
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	status, result := humanHTTP(t, c, false, "POST", "/v1/access/pair/start", map[string]string{"name": "laptop", "publicKey": base64.RawStdEncoding.EncodeToString(public)}, "", "")
	// Bootstrap must bind to a genuine existing request, not an invented identifier.
	if status != 200 {
		t.Fatal(status, result)
	}
	return result
}

// TestOwnerSetupRequiresLocalAuthorityAndMatchingController checks the first-owner trust boundary.
func TestOwnerSetupRequiresLocalAuthorityAndMatchingController(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	pending := setupPendingDevice(t, c)
	input := model.OwnerSetupRequest{URL: "https://" + c.state.Hostname, RequestID: pending["id"].(string), Name: "Owner"}
	// First-time discovery may advertise setup but must not issue authority itself.
	if pending["ownerSetupRequired"] != true {
		t.Fatal("missing first-owner hint")
	}
	status, _ := humanHTTP(t, c, false, "POST", "/v1/access/setup", input, "", "")
	// A public visitor cannot turn an unowned controller into their own account.
	if status == 200 || len(c.access.Bootstraps) != 0 {
		t.Fatal("public owner bootstrap accepted")
	}
	wrong := input
	wrong.URL = "https://wrong.example.com"
	status, _ = humanHTTP(t, c, true, "POST", "/v1/access/setup", wrong, "", "")
	// SSH access to the wrong host cannot issue a misleading link.
	if status == 200 || len(c.access.Bootstraps) != 0 {
		t.Fatal("wrong controller accepted")
	}
	wrong = input
	wrong.RequestID = "absent"
	status, _ = humanHTTP(t, c, true, "POST", "/v1/access/setup", wrong, "", "")
	// An unrelated request cannot borrow an initial bootstrap capability.
	if status == 200 {
		t.Fatal("missing device accepted")
	}
	status, result := humanHTTP(t, c, true, "POST", "/v1/access/setup", input, "", "")
	// Successful bootstrap creates an enrollment opportunity, not a logged-in device.
	if status != 200 || result["status"] != "enroll" || len(c.state.Clients) != 0 {
		t.Fatal(status, result)
	}
	assertOwnerSetupLink(t, c, input.RequestID, result["url"].(string))
}

// assertOwnerSetupLink checks the returned secret is usable only through ordinary verified registration.
func assertOwnerSetupLink(t *testing.T, c *Controller, id, link string) {
	t.Helper()
	parsed, _ := url.Parse(link)
	values, _ := url.ParseQuery(parsed.Fragment)
	// The next browser page must review the exact original device after registration.
	if parsed.Path != "/security" || values.Get("request") != id || values.Get("enroll") == "" {
		t.Fatal("bad enrollment link")
	}
	status, _ := browserJSON(t, c, "/v1/access/passkey/register/begin", map[string]string{"token": values.Get("enroll")}, "")
	// The new link follows the existing passkey registration verifier, not a bypass.
	if status != 200 {
		t.Fatal("bootstrap registration rejected", status)
	}
}

// TestOwnerSetupDoesNotMintLinksForExistingOwners keeps repeated SSH connections least-privileged.
func TestOwnerSetupDoesNotMintLinksForExistingOwners(t *testing.T) {
	f := registerBrowserFixture(t)
	pending := setupPendingDevice(t, f.c)
	input := model.OwnerSetupRequest{URL: "https://" + f.c.state.Hostname, RequestID: pending["id"].(string), Name: "Another owner"}
	status, result := humanHTTP(t, f.c, true, "POST", "/v1/access/setup", input, "", "")
	// An initialized controller should proceed to approval without enrolling an additional owner.
	if status != 200 || result["status"] != "ready" || result["url"] != nil || len(f.c.access.Bootstraps) != 0 || pending["ownerSetupRequired"] != false {
		t.Fatal(status, result)
	}
}
