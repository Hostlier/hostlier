package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretProject appends authored flat secret declarations to a valid ordinary project.
func secretProject(t *testing.T, bindings string) string {
	t.Helper()
	dir := project(t)
	path := filepath.Join(dir, ".hostlier")
	data, _ := os.ReadFile(path)
	// Fixture values are never real credentials.
	if err := os.WriteFile(path, append(data, []byte("secrets:\n"+bindings)...), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestFlatProjectSecretsSyncBeforePlanning exercises the real command and its redacted output.
func TestFlatProjectSecretsSyncBeforePlanning(t *testing.T) {
	// Both commands must resolve and upload identical inputs before preparing a plan.
	for _, command := range []string{"plan", "deploy"} {
		// Isolate command state and its private input snapshot.
		t.Run(command, func(t *testing.T) {
			dir := secretProject(t, "  LITERAL: 'literal-private-marker'\n  LOCAL: $TEST_LOCAL_TOKEN\n  STORED: secret://existing-token\n")
			fake := &fakeClient{}
			var out, stderr bytes.Buffer
			err := RunWithOptions(context.Background(), []string{command, "--detach"}, strings.NewReader(""), &out, &stderr, Options{CWD: dir, Home: t.TempDir(), Client: fake, Environment: map[string]string{"TEST_LOCAL_TOKEN": "environment-private-marker"}})
			// Automatic synchronization must need no separate secret command.
			if err != nil {
				t.Fatal(err)
			}
			assertSecretPlanCalls(t, fake.calls)
			// Neither output stream may expose plaintext source values.
			for _, private := range []string{"literal-private-marker", "environment-private-marker"} {
				// The preview includes public references only.
				if strings.Contains(out.String()+stderr.String(), private) {
					t.Fatal("secret leaked to command output")
				}
			}
		})
	}
}

// assertSecretPlanCalls checks ordering, exact upload bytes and a reference-only plan payload.
func assertSecretPlanCalls(t *testing.T, calls []call) {
	t.Helper()
	// Two secret uploads must precede the first plan request.
	if len(calls) < 3 || calls[0].method != "PUT" || calls[1].method != "PUT" || calls[2].path != "/v1/plans" {
		t.Fatal("incorrect secret synchronization ordering")
	}
	// Each declared source must supply its exact resolved value once.
	for i, value := range []string{"literal-private-marker", "environment-private-marker"} {
		// Idempotency identities must accompany normal secret uploads.
		if calls[i].body.(map[string]any)["value"] != value || calls[i].body.(map[string]any)["requestId"] == "" {
			t.Fatal("incorrect secret upload")
		}
	}
	assertPlanSecretReferences(t, calls)
}

// TestFlatProjectSecretValidationIsOffline checks missing inputs before any remote side effects.
func TestFlatProjectSecretValidationIsOffline(t *testing.T) {
	// Empty and unavailable sources are invalid, while valid sources remain offline under validate.
	for _, value := range []string{"", "present", strings.Repeat("x", 65537)} {
		fake := &fakeClient{}
		dir := secretProject(t, "  TOKEN: $TEST_LOCAL_TOKEN\n")
		_, err := invoke([]string{"validate"}, "", Options{CWD: dir, Home: t.TempDir(), Client: fake, Environment: map[string]string{"TEST_LOCAL_TOKEN": value}})
		// Validation reads local inputs but cannot write or query controller secrets.
		if len(fake.calls) != 0 || (err == nil) != (value == "present") {
			t.Fatal("invalid offline source handling", err)
		}
	}
	fake := &fakeClient{}
	dir := secretProject(t, "  FIRST: literal-private-marker\n  MISSING: $NOT_SET\n")
	_, err := invoke([]string{"deploy", "--detach"}, "", Options{CWD: dir, Client: fake, Environment: map[string]string{}})
	// A missing later source cannot leave an earlier source partially uploaded.
	if err == nil || len(fake.calls) != 0 {
		t.Fatal("incomplete inputs caused side effects")
	}
}

// TestStoredProjectSecretsDoNotUpload preserves reference-only deployments using explicit syntax.
func TestStoredProjectSecretsDoNotUpload(t *testing.T) {
	fake := &fakeClient{}
	dir := secretProject(t, "  TOKEN: secret://already-stored\n")
	_, err := invoke([]string{"plan"}, "", Options{CWD: dir, Client: fake})
	// Existing references must not be mistaken for literal token values.
	if err != nil || len(fake.calls) != 1 || fake.calls[0].path != "/v1/plans" {
		t.Fatal("stored reference triggered an upload", err)
	}
}

// assertPlanSecretReferences checks the normalized references and absence of private payloads.
func assertPlanSecretReferences(t *testing.T, calls []call) {
	t.Helper()
	p := calls[2].body.(map[string]any)["spec"].(model.Project)
	// The planned references must point to the names uploaded by this invocation.
	if calls[0].path != "/v1/secrets/"+p.Secrets["LITERAL"] || calls[1].path != "/v1/secrets/"+p.Secrets["LOCAL"] || p.Secrets["STORED"] != "existing-token" {
		t.Fatal("plan secret references differ from uploaded names")
	}
	data, _ := json.Marshal(calls[2].body)
	// Private payloads must never enter the general planning API.
	if strings.Contains(string(data), "private-marker") {
		t.Fatal("private value entered the deployment plan")
	}
}
