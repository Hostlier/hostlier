package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"hostlier/internal/model"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rollbackTransport models authoritative daemon responses without starting unrelated external services.
type rollbackTransport func(*http.Request) (*http.Response, error)

// RoundTrip passes exactly one daemon request into the safety fixture.
func (f rollbackTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// dockerSafetyRuntime provides a private in-memory Docker API with normal production response decoding.
func dockerSafetyRuntime(t *testing.T, response func(*http.Request) (int, string)) *Runtime {
	t.Helper()
	return &Runtime{options: Options{StateDir: t.TempDir()}, owner: "test", docker: &http.Client{Transport: rollbackTransport(
		// Decode fixture responses through the same HTTP boundary used against Docker.
		func(req *http.Request) (*http.Response, error) {
			status, body := response(req)
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
}

// TestDockerRollbackDoesNotResumeSourceAfterFailedCandidateRemoval prevents concurrent PVC writers.
func TestDockerRollbackDoesNotResumeSourceAfterFailedCandidateRemoval(t *testing.T) {
	started := false
	r := dockerSafetyRuntime(t,
		// The deletion fault leaves candidate ownership unresolved.
		func(req *http.Request) (int, string) {
			// Candidate cleanup is unavailable while its process could still be writing.
			if req.Method == "DELETE" {
				return 500, `{"message":"unavailable"}`
			}
			// Source restart must never occur after that unverified cleanup outcome.
			if strings.Contains(req.URL.Path, "/start") {
				started = true
			}
			return 200, `{}`
		})
	spec := model.Project{Version: 1, Name: "app"}
	d := &dockerDeployment{runtime: r, ctx: context.Background(), spec: spec, candidate: dockerContainer{ID: "candidate"}, old: appRecord{Deployment: model.Deployment{Name: "app", RuntimeID: "source", Status: "running"}}, hasOld: true, oldStopped: true}
	// Preserve the execution intent when rollback cannot prove the candidate released its data.
	if err := r.beginIntent(spec, "operation"); err != nil {
		t.Fatal(err)
	}
	d.rollback()
	// A failed deletion must leave the previous writer stopped and its journal intact.
	if started {
		t.Fatal("source restarted while candidate removal failed")
	}
	// The next approved operation needs the same durable unknown-effect evidence.
	if intent, err := r.readIntent("app"); err != nil || intent == nil {
		t.Fatal("rollback lost its unresolved execution journal", err)
	}
}

// TestK3sRollbackWaitsForCandidatePodsBeforeSourceScale observes strict single-writer handoff order.
func TestK3sRollbackWaitsForCandidatePodsBeforeSourceScale(t *testing.T) {
	// Both successful termination and failed termination must preserve the writer ordering boundary.
	for _, mode := range []string{"terminated", "wait-failed", "delete-failed"} {
		// Each attempt has an independent observed command sequence.
		t.Run(mode, func(t *testing.T) { testK3sRollbackWriterOrder(t, mode) })
	}
}

// testK3sRollbackWriterOrder models Kubernetes deletion returning before pod termination.
func testK3sRollbackWriterOrder(t *testing.T, mode string) {
	t.Helper()
	base := dockerSafetyRuntime(t, rollbackNoopHTTP)
	base.web = base.docker
	k := &K3s{base: base}
	waited, scaled := false, false
	k.options.KubeRun =
		// Observe command ordering without allowing asynchronous pod deletion to imply absence.
		func(_ context.Context, args []string, _ []byte) ([]byte, error) {
			command := strings.Join(args, " ")
			// Normal owned-object inspection authorizes deletion but does not establish pod absence.
			if strings.Contains(command, "get ") {
				return json.Marshal(map[string]any{"metadata": map[string]any{"labels": k.base.labels("app")}})
			}
			// A failed Deployment deletion cannot authorize waiting assumptions or source reactivation.
			if strings.Contains(command, "delete deployment") && mode == "delete-failed" {
				return nil, errors.New("candidate deletion unavailable")
			}
			// Only successful pod deletion observation authorizes the subsequent source scale.
			if strings.Contains(command, "wait --for=delete pods") {
				waited = true
				// A failed wait cannot authorize old-writer reactivation.
				if mode == "wait-failed" {
					return nil, errors.New("pods still terminating")
				}
			}
			// Any earlier source resume would violate local-path single-writer semantics.
			if strings.Contains(command, "scale deployment source") {
				scaled = true
				// Source restart must follow the successful candidate-pod termination boundary.
				if !waited {
					t.Error("source scale preceded candidate pod termination")
				}
			}
			return nil, nil
		}
	spec := model.Project{Version: 1, Name: "app", Image: "nginx@sha256:" + strings.Repeat("a", 64), Port: 80, Route: "app.example.com"}
	d := &k3sDeployment{runtime: k, ctx: context.Background(), spec: spec, s: state{Apps: map[string]appRecord{}}, version: "candidate", namespace: "namespace", old: appRecord{Deployment: model.Deployment{RuntimeID: "source"}, Spec: spec}, hasOld: true, oldStopped: true}
	// Run the exact production rollback path with edge activation intentionally not yet reached.
	d.rollback()
	// Failure to observe pod deletion must prevent every source restart.
	if scaled != (mode == "terminated") {
		t.Fatalf("source scale=%v after cleanup mode=%s", scaled, mode)
	}
}

// TestRecoveredPendingDrainRequiresBothWritersAbsent fences the clean-host exception narrowly.
func TestRecoveredPendingDrainRequiresBothWritersAbsent(t *testing.T) {
	// Owned live sources, daemon failures and foreign ownership all remain blocked.
	for _, source := range []string{"absent", "owned", "foreign", "daemon"} {
		// Each source observation uses fresh durable restored metadata.
		t.Run(source, func(t *testing.T) { testRecoveredPendingDrain(t, source) })
	}
}

// testRecoveredPendingDrain uses authoritative daemon absence plus the original restore-location marker.
func testRecoveredPendingDrain(t *testing.T, source string) {
	t.Helper()
	r := dockerSafetyRuntime(t,
		// Each response reports an independently selected former-source observation.
		func(req *http.Request) (int, string) { return recoveredDrainResponse(req, source) })
	raw, _ := json.Marshal(model.RecoveryOrigin{StateDirectory: r.options.StateDir})
	// Only an owner-protected original-path marker grants the clean-host absence exception.
	if err := os.WriteFile(filepath.Join(r.options.StateDir, "recovery-origin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	spec := model.Project{Version: 1, Name: "app"}
	app := appRecord{Deployment: model.Deployment{Name: "app", RuntimeID: "active", Status: "stopped"}, PreviousRuntimeID: "source", Spec: spec}
	s := state{Apps: map[string]appRecord{"app": app}}
	// The retained marker must survive every failed or ambiguous ownership observation.
	if err := r.save(s); err != nil {
		t.Fatal(err)
	}
	d := &dockerDeployment{runtime: r, ctx: context.Background(), spec: spec, s: s, old: app, hasOld: true}
	err := d.recoverDrain()
	assertRecoveredDrain(t, source, d, err)
}

// recoveredDrainResponse independently models each old source observation after active-writer absence.
func recoveredDrainResponse(req *http.Request, source string) (int, string) {
	// The active committed candidate is authoritatively absent on this restored host.
	if !strings.Contains(req.URL.Path, "/source/") {
		return 404, `{}`
	}
	// Source existence, ownership and API failure remain distinct from proven absence.
	switch source {
	// A live source remains protected even when it still belongs to the restored server.
	case "owned":
		return 200, `{"Id":"source","Config":{"Labels":{"io.hostlier.managed":"true","io.hostlier.owner":"test","io.hostlier.app":"app"}}}`
	// Foreign sources never become cleanup authorization.
	case "foreign":
		return 200, `{"Id":"source","Config":{"Labels":{"io.hostlier.owner":"foreign"}}}`
	// Daemon failure proves neither writer absence nor safe data handoff.
	case "daemon":
		return 500, `{}`
	}
	return 404, `{}`
}

// assertRecoveredDrain validates clean-host success and preservation of every ambiguous marker.
func assertRecoveredDrain(t *testing.T, source string, d *dockerDeployment, err error) {
	t.Helper()
	// Both absent writers permit redeployment while preserving intentionally stopped desired capacity.
	if source == "absent" {
		// Only writer identities are cleared; desired stopped state remains authoritative.
		if err != nil || d.old.RuntimeID != "" || d.old.PreviousRuntimeID != "" || d.old.Status != "stopped" {
			t.Fatal("clean-host recovery failed", err, d.old)
		}
		return
	}
	// No surviving or unverified source can be silently forgotten.
	if err == nil || d.old.PreviousRuntimeID != "source" {
		t.Fatal("unsafe recovered drain", err)
	}
}

// rollbackNoopHTTP permits unrelated rollback edge cleanup without masking writer command ordering.
func rollbackNoopHTTP(*http.Request) (int, string) { return 200, `{}` }
