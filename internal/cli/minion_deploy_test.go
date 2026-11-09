package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/artifact"
	"hostlier/internal/model"
)

// TestMinionDeployRejectsMismatchedProject catches an accidental deploy from a neighbouring app before installation.
func TestMinionDeployRejectsMismatchedProject(t *testing.T) {
	root := project(t)
	writeRuntimeFixture(t, filepath.Join(root, "minion.json"), []byte(`{"name":"another-app"}`))
	remote := &fakeClient{}
	_, err := invoke([]string{"deploy"}, "", Options{CWD: root, Client: remote})
	// A mismatch must not contact the controller.
	if err == nil || !strings.Contains(err.Error(), "local container source") || len(remote.calls) != 0 {
		t.Fatal(err, remote.calls)
	}
	// Explicit artifact selection must continue to skip Minion preparation.
	if _, err = invoke([]string{"deploy", "--file", ".hostlier", "--detach"}, "", Options{CWD: root, Client: remote}); err != nil {
		t.Fatal(err)
	}
}

// TestMinionAutomaticRelease exercises a released compiler, npm installation, Docker build, real artifact store and OAuth relay.
// Only remote controller decisions and the Engine's read-only contract catalog are fixtures.
func TestMinionAutomaticRelease(t *testing.T) {
	archive := os.Getenv("HOSTLIER_TEST_MINION_RELEASE")
	// Keep normal Go tests independent of local Docker and npm.
	if archive == "" {
		t.Skip("set HOSTLIER_TEST_MINION_RELEASE to a Minion tarball; requires Node, npm and Docker")
	}
	// Retain coverage of earlier projects that rely on automatic detection.
	t.Run("auto-detected", func(t *testing.T) { verifyMinionRelease(t, archive, nil) })
	// An explicit hook binary enables the same checks against the packaged CLI contract.
	if hook := os.Getenv("HOSTLIER_TEST_MINION_HOOK"); hook != "" {
		// Install the hook in an independent local package store.
		t.Run("package-hooks", func(t *testing.T) {
			state, err := filepath.EvalSymlinks(t.TempDir())
			// The protected installation rejects symlinked temporary ancestors.
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"package", "install", "minion", "--file", hook, "--state-dir", state}
			// The package must enter through the same installer as a user-supplied executable.
			if _, err := invoke(args, "", Options{Home: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			verifyMinionRelease(t, archive, []string{"--state-dir", state})
		})
	}
}

// verifyMinionRelease runs identical consent, packaging and immutable-image assertions for both entry points.
func verifyMinionRelease(t *testing.T, archive string, selection []string) {
	t.Helper()
	catalog := httptest.NewServer(http.HandlerFunc(minionTestCatalog))
	defer catalog.Close()
	root := t.TempDir()
	writeRuntimeFixture(t, filepath.Join(root, "package.json"), []byte(`{"name":"mini","private":true,"type":"module"}`))
	writeRuntimeFixture(t, filepath.Join(root, "app.ts"), []byte(`import {buildMinion} from '@fused/minion'; export default buildMinion({actions:{}});`))
	cfg := fmt.Sprintf(`{"name":"mini","entry":"app.ts","route":"mini.example.com","fused":{"engineUrl":%q,"grpcUrl":%q,"tokenSecret":"mini-token","bucket":"demo","version":"1.0.0","services":{"crm":{"service":"crm","version":"v1","operations":["read"]}}}}`, catalog.URL, catalog.URL)
	writeRuntimeFixture(t, filepath.Join(root, "minion.json"), []byte(cfg))
	opts := Options{CWD: root, Home: t.TempDir(), Environment: map[string]string{"HOSTLIER_MINION_PACKAGE": archive}}
	f := &minionReleaseFixture{artifacts: containerPlanFixture{store: artifact.Store{Root: t.TempDir(), UploadOwner: "local-test"}}, connections: map[string]runtimeConnectionStatus{}}
	opts.Client = &fakeClient{reply: f.reply}
	// Init must save usable local defaults before provisioning.
	initArgs := []string{"init"}
	// The package path is selected exactly once, during init.
	if len(selection) != 0 {
		// A Go test process is not the shipped installer. Install the exact framework under test first.
		if _, err := invoke([]string{"minion", "install"}, "", opts); err != nil {
			t.Fatal(err)
		}
		initArgs = append(append(initArgs, "--package", "minion"), selection...)
	} else { // Explicitly exercise guided source detection.
		initArgs = append(initArgs, "--detect")
	}
	// Both initializers must produce a source project the subsequent deploy reads.
	if _, err := invoke(initArgs, "", opts); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".hostlier")
	original, err := os.ReadFile(path)
	// An unresolved identity must still leave an editable project.
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.ReplaceAll(original, []byte("mini.example.com"), []byte("edited.example.com"))
	writeRuntimeFixture(t, path, edited)
	// The same permissions survive source edits; the second release must build the newer code.
	for i := 0; i < 2; i++ {
		var diagnostics bytes.Buffer
		err := RunWithOptions(context.Background(), append([]string{"deploy", "--detach"}, selection...), strings.NewReader(""), io.Discard, &diagnostics, opts)
		// Include compiler and Docker diagnostics if the release fails.
		if err != nil {
			t.Fatalf("deploy %d: %v\n%s", i, err, diagnostics.String())
		}
		writeRuntimeFixture(t, filepath.Join(root, "app.ts"), []byte(`import {buildMinion} from '@fused/minion'; console.info("changed release"); export default buildMinion({actions:{}});`))
	}
	assertMinionReleases(t, path, edited, f)
	checkMinionReleaseChanges(t, root, opts, f, selection)
}

// checkMinionReleaseChanges distinguishes a new release under a grant from a new requested bucket.
func checkMinionReleaseChanges(t *testing.T, root string, opts Options, f *minionReleaseFixture, selection []string) {
	t.Helper()
	path := filepath.Join(root, "minion.json")
	config := readWizardConfig(t, root)
	config["fused"].(map[string]any)["version"] = "2.0.0"
	updated, err := json.Marshal(config)
	// Keep all setup defaults when editing only the Fused release.
	if err != nil {
		t.Fatal(err)
	}
	writeRuntimeFixture(t, path, updated)
	// A release-only change must use the app-bound deployment route.
	if _, err := invoke(append([]string{"deploy", "--detach"}, selection...), "", opts); err != nil || f.consents != 1 || f.versions != 1 || f.applied != 3 {
		t.Fatal(err, f.consents, f.versions, f.applied)
	}
	f.deny = true
	writeRuntimeFixture(t, path, []byte(strings.Replace(string(updated), `"demo"`, `"extra"`, 1)))
	// Refusing access to the new bucket must prevent an artifact upload or app deployment.
	if _, err := invoke(append([]string{"deploy", "--detach"}, selection...), "", opts); err == nil || f.consents != 2 || f.applied != 3 || len(f.images) != 3 {
		t.Fatal(err, f.consents, f.applied, f.images)
	}
}

// assertMinionReleases checks retained topology, fresh images and grant reuse.
func assertMinionReleases(t *testing.T, path string, edited []byte, f *minionReleaseFixture) {
	t.Helper()
	after, err := os.ReadFile(path)
	// The changed code must produce a different uploaded image.
	if err != nil || !bytes.Equal(edited, after) || f.consents != 1 || f.applied != 2 || len(f.images) != 2 || f.images[0] == f.images[1] {
		t.Fatal("incorrect consent, deployment, fresh image or source preservation", err, f.consents, f.applied, f.images)
	}
}

// minionTestCatalog returns just the selected operation's synthetic version and contract metadata.
func minionTestCatalog(w http.ResponseWriter, r *http.Request) {
	const service = "11111111-1111-4111-8111-111111111111"
	const version = "22222222-2222-4222-8222-222222222222"
	var q struct{ Query string }
	_ = json.NewDecoder(r.Body).Decode(&q)
	// Answer version discovery separately from operation discovery.
	if strings.Contains(q.Query, "MinionVersions") {
		fmt.Fprintf(w, `{"data":{"serviceVersions":[{"id":%q,"service_id":%q,"name":"v1","contract_version":2,"required_capabilities":[]}]}}`, version, service)
		return
	}
	fmt.Fprintf(w, `{"data":{"serviceRuntimeContracts":[{"service_id":%q,"service_version_id":%q,"version":"v1","operations":[{"id":"33333333-3333-4333-8333-333333333333","name":"read","parameters":[],"responses":{}}]}]}}`, service, version)
}

// minionReleaseFixture verifies the order and values crossing the controller boundary without taking a live grant.
type minionReleaseFixture struct {
	artifacts                   containerPlanFixture
	connections                 map[string]runtimeConnectionStatus
	pending                     runtimeConnectionStatus
	consents, applied, versions int
	deny                        bool
	images                      []string
}

// reply simulates only operator consent and remote application outcomes, and verifies actual uploaded image bytes.
func (f *minionReleaseFixture) reply(c call) (any, error) {
	// Only the existing controller endpoints participate.
	switch {
	// Expose only public grant metadata.
	case c.path == "/v1/fused":
		return map[string]any{"connections": f.connections}, nil
	// An unbound manifest starts one consent request.
	case strings.HasSuffix(c.path, "/connect"):
		return f.connect(c)
	// A version update reuses the same management connection.
	case strings.HasSuffix(c.path, "/deploy"):
		b := c.body.(map[string]any)
		f.versions++
		prior := f.connections[strings.Split(c.path, "/")[3]]
		// Never replace a prior release credential in place.
		if prior.Runtime == nil || prior.Runtime.TokenSecret == b["secretName"] {
			return nil, fmt.Errorf("new release needs previous grant and fresh secret")
		}
		prior.ManifestDigest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(b["manifest"].(string))))
		prior.Runtime.TokenSecret = b["secretName"].(string)
		f.connections[strings.Split(c.path, "/")[3]] = prior
		return map[string]any{}, nil
	// The relay completes the pending request before packaging.
	case strings.HasSuffix(c.path, "/complete"):
		// A refused grant cannot set the connected state.
		if f.deny {
			return nil, fmt.Errorf("consent declined")
		}
		f.connections[strings.Split(c.path, "/")[3]] = f.pending
		return map[string]any{}, nil
	// Only immutable, bound artifacts may reach review.
	case c.path == "/v1/plans":
		return f.plan(c)
	// Only approved plans count as applied releases.
	case strings.HasSuffix(c.path, "/apply"):
		f.applied++
		return map[string]any{"operationId": "test-operation"}, nil
	// Exercise the real artifact transport and integrity checks.
	default:
		return f.artifacts.reply(c)
	}
}

// connect drives the real loopback relay for a synthetic browser decision.
func (f *minionReleaseFixture) connect(c call) (any, error) {
	b := c.body.(map[string]any)
	digest := sha256.Sum256([]byte(b["manifest"].(string)))
	f.pending = runtimeConnectionStatus{Status: "connected", EngineURL: b["engineUrl"].(string), ManifestDigest: fmt.Sprintf("sha256:%x", digest), Runtime: &model.FusedConnection{AppID: "11111111-2222-4333-8444-555555555555", GRPCURL: b["grpcUrl"].(string), TokenSecret: b["secretName"].(string)}}
	f.consents++
	// The loopback relay receives a callback as it would from the browser.
	go func(callback string) {
		resp, err := http.Get(callback + "?state=fixture&code=approved")
		// Release the synthetic browser response.
		if err == nil {
			resp.Body.Close()
		}
	}(b["redirectUri"].(string))
	return map[string]any{"authorizationUrl": f.pending.EngineURL + "/authorize?state=fixture", "expiresAt": time.Now().Add(time.Minute)}, nil
}

// plan checks the final identity and route before inspecting the uploaded archive.
func (f *minionReleaseFixture) plan(c call) (any, error) {
	p := c.body.(map[string]any)["spec"].(model.Project)
	// Verify both the consent identity and the edited route.
	if p.Fused == nil || p.Fused.AppID != f.pending.Runtime.AppID || p.Route != "edited.example.com" {
		return nil, fmt.Errorf("incorrect plan identity or route")
	}
	f.images = append(f.images, p.Image)
	return f.artifacts.plan(c)
}

// TestMinionDeployBuildDefaults covers a dev scaffold using the same temporary fields as a released compiler.
func TestMinionDeployBuildDefaults(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": `{"name":"mini","entry":"app.ts"}`})
	setup, err := readMinionInitSetup(root)
	if err != nil {
		t.Fatal(err)
	}
	s := &session{cwd: root, flags: map[string]string{}}
	if err := s.configureMinionDeploy(setup); err == nil || !strings.Contains(err.Error(), "--route") {
		t.Fatal("expected hostname guidance", err)
	}
	s.flags["route"] = "mini.example.com"
	if err := s.configureMinionDeploy(setup); err != nil {
		t.Fatal(err)
	}
	if setup.value("image") != "" || setup.value("baseImage") != "" || setup.value("route") != s.flags["route"] {
		t.Fatal("incorrect compiler settings")
	}
	if len(readWizardConfig(t, root)) != 2 {
		t.Fatal("deploy changed the source config")
	}
	setup.set("image", "custom/mini:v2")
	if err := s.configureMinionDeploy(setup); err != nil || setup.value("image") != "custom/mini:v2" {
		t.Fatal("failed to retain an explicit version", err)
	}

}
