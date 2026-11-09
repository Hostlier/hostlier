package runtime

import (
	"context"
	"encoding/json"
	"hostlier/internal/model"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCleanupRetrySurvivesRestart retries exact failed objects in dependency order after a daemon error.
func TestCleanupRetrySurvivesRestart(t *testing.T) {
	deleted := map[string]bool{}
	fail := true
	r := retryCleanupRuntime(t, deleted, &fail)
	// Check the fixture or cleanup invariant before proceeding.
	if err := r.writeCleanup(failedGroupCleanup{App: "app", Containers: []string{"sidecar", "candidate", "anchor"}}); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := r.CleanupFailed(context.Background()); err == nil || deleted["anchor"] {
		t.Fatal("anchor removed before failed member", err)
	}
	fail = false
	restarted := &Runtime{options: r.options, owner: r.owner, docker: r.docker}
	count, err := restarted.CleanupFailed(context.Background())
	// Check the fixture or cleanup invariant before proceeding.
	if err != nil || count != 1 || !deleted["candidate"] || !deleted["anchor"] {
		t.Fatal(count, deleted, err)
	}
	files, err := os.ReadDir(filepath.Join(r.options.StateDir, "runtime-cleanup"))
	// Check the fixture or cleanup invariant before proceeding.
	if err != nil || len(files) != 0 {
		t.Fatal("completed journal retained", files, err)
	}
}

func TestDeleteRemovesSupersededContainersBeforeNetworkHolders(t *testing.T) {
	var removed []string
	var r *Runtime
	r = dockerSafetyRuntime(t, func(req *http.Request) (int, string) {
		if req.Method == "GET" && req.URL.Path == "/containers/json" {
			items := []map[string]any{
				{"Id": "anchor", "Names": []string{"/hostlier-test-app-old-network"}, "Labels": r.labels("app")},
				{"Id": "sidecar", "Names": []string{"/hostlier-test-app-old-sidecar-storage"}, "Labels": r.labels("app")},
				{"Id": "primary", "Names": []string{"/hostlier-test-app-old"}, "Labels": r.labels("app")},
			}
			body, _ := json.Marshal(items)
			return 200, string(body)
		}
		if req.Method == "DELETE" {
			removed = append(removed, strings.TrimPrefix(req.URL.Path, "/containers/"))
			return 204, ""
		}
		return 500, `{}`
	})
	if err := r.removeAppContainers(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(removed, ","); got != "sidecar,primary,anchor" {
		t.Fatal("container deletion order", got)
	}
}

func TestDeleteRejectsForeignContainerBeforeAnyRemoval(t *testing.T) {
	removed := false
	r := dockerSafetyRuntime(t, func(req *http.Request) (int, string) {
		if req.Method == "GET" {
			return 200, `[{"Id":"owned","Labels":{"io.hostlier.managed":"true","io.hostlier.owner":"test","io.hostlier.app":"app"}},{"Id":"foreign","Labels":{"io.hostlier.owner":"other"}}]`
		}
		removed = true
		return 204, ""
	})
	if err := r.removeAppContainers(context.Background(), "app"); err == nil || removed {
		t.Fatal("unowned inventory was mutated", err)
	}
}

func TestStopGroupReleasesNamespaceWithoutDeletingResources(t *testing.T) {
	var actions []string
	var r *Runtime
	r = dockerSafetyRuntime(t, func(req *http.Request) (int, string) {
		if req.URL.Host == "caddy" {
			return 200, `{}`
		}
		if req.Method == "DELETE" {
			t.Error("stop deleted a resource")
			return 500, `{}`
		}
		if req.Method == "POST" {
			actions = append(actions, req.URL.Path)
			return 204, ""
		}
		id := strings.Split(strings.TrimPrefix(req.URL.Path, "/containers/"), "/")[0]
		body, _ := json.Marshal(map[string]any{"Id": id, "Config": map[string]any{"Labels": r.labels("app")}, "State": map[string]any{"Running": true}})
		return 200, string(body)
	})
	r.options.CaddyAdmin = "http://caddy"
	r.web = r.docker
	spec := model.Project{Version: 2, Name: "app", Sidecars: map[string]model.Project{"storage": {Name: "storage"}}}
	app := appRecord{Deployment: model.Deployment{Name: "app", RuntimeID: "primary", Status: "running"}, Spec: spec, Anchor: "anchor", Members: map[string]string{"storage": "sidecar"}}
	_, err := r.stopGroupForAction(context.Background(), "app", "stop", app, state{Apps: map[string]appRecord{"app": app}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(actions, ","); got != "/containers/primary/stop,/containers/sidecar/stop,/containers/anchor/stop" {
		t.Fatal("stop order", got)
	}
}

// retryCleanupRuntime simulates deletion failure before the namespace holder may be released.
func retryCleanupRuntime(t *testing.T, deleted map[string]bool, fail *bool) *Runtime {
	t.Helper()
	var r *Runtime
	r = dockerSafetyRuntime(t,
		// Supply controlled state for this cleanup boundary.
		func(req *http.Request) (int, string) {
			id := strings.Split(strings.TrimPrefix(req.URL.Path, "/containers/"), "/")[0]
			// Check the fixture or cleanup invariant before proceeding.
			if deleted[id] {
				return 404, `{}`
			}
			// Check the fixture or cleanup invariant before proceeding.
			if req.Method == "DELETE" {
				// Check the fixture or cleanup invariant before proceeding.
				if id == "candidate" && *fail {
					return 500, `{}`
				}
				deleted[id] = true
				return 204, ""
			}
			body, _ := json.Marshal(map[string]any{"Id": id, "Config": map[string]any{"Labels": r.labels("app")}})
			return 200, string(body)
		})
	return r
}

// TestRollbackCleansAnchorWithoutPrimary covers failure between namespace creation and primary creation.
func TestRollbackCleansAnchorWithoutPrimary(t *testing.T) {
	deleted := false
	var r *Runtime
	r = dockerSafetyRuntime(t,
		// Supply controlled state for this cleanup boundary.
		func(req *http.Request) (int, string) {
			// Check the fixture or cleanup invariant before proceeding.
			if req.Context().Err() != nil {
				t.Error("cleanup reused cancelled context")
			}
			// Check the fixture or cleanup invariant before proceeding.
			if req.Method == "DELETE" {
				deleted = true
				return 204, ""
			}
			body, _ := json.Marshal(map[string]any{"Id": "anchor", "Config": map[string]any{"Labels": r.labels("app")}})
			return 200, string(body)
		})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := dockerDeployment{runtime: r, ctx: ctx, spec: model.Project{Name: "app", Version: 2}, anchor: "anchor"}
	d.rollback()
	// Check the fixture or cleanup invariant before proceeding.
	if !deleted {
		t.Fatal("empty primary suppressed anchor cleanup")
	}
}

// TestCleanupProtectsAdoptedAndForeignContainers refuses later live identities and unrelated Docker ownership.
func TestCleanupProtectsAdoptedAndForeignContainers(t *testing.T) {
	requests := 0
	r := dockerSafetyRuntime(t,
		// Supply controlled state for this cleanup boundary.
		func(req *http.Request) (int, string) {
			requests++
			return 200, `{"Config":{"Labels":{"io.hostlier.owner":"other"}}}`
		})
	s := state{Apps: map[string]appRecord{"app": {Deployment: model.Deployment{RuntimeID: "adopted"}}}, Retained: map[string]appRecord{}}
	// Check the fixture or cleanup invariant before proceeding.
	if err := r.save(s); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if err := r.writeCleanup(failedGroupCleanup{App: "app", Containers: []string{"adopted"}}); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := r.CleanupFailed(context.Background()); err != nil || requests != 0 {
		t.Fatal("adopted container touched", err, requests)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if err := r.writeCleanup(failedGroupCleanup{App: "app", Containers: []string{"foreign"}}); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := r.CleanupFailed(context.Background()); err == nil || requests != 1 {
		t.Fatal("foreign ownership ignored", err, requests)
	}
}

// TestCleanupNetworkOwnership permits only an empty owned network without retained application references.
func TestCleanupNetworkOwnership(t *testing.T) {
	// Check the fixture or cleanup invariant before proceeding.
	for _, mode := range []string{"empty", "consumer", "foreign", "retained"} {
		t.Run(mode,
			// Supply controlled state for this cleanup boundary.
			func(t *testing.T) {
				deleted := false
				var r *Runtime
				r = dockerSafetyRuntime(t,
					// Supply controlled state for this cleanup boundary.
					func(req *http.Request) (int, string) {
						// Check the fixture or cleanup invariant before proceeding.
						if req.Method == "DELETE" {
							deleted = true
							return 204, ""
						}
						labels := r.labels("app")
						// Check the fixture or cleanup invariant before proceeding.
						if mode == "foreign" {
							labels["io.hostlier.owner"] = "other"
						}
						consumers := map[string]any{}
						// Check the fixture or cleanup invariant before proceeding.
						if mode == "consumer" {
							consumers["other-app"] = map[string]any{}
						}
						body, _ := json.Marshal(map[string]any{"Labels": labels, "Containers": consumers})
						return 200, string(body)
					})
				job := failedGroupCleanup{App: "app", Network: r.applicationNetwork("app")}
				_ = r.cleanGroupNetwork(context.Background(), job, map[string]bool{"network:app": mode == "retained"})
				// Check the fixture or cleanup invariant before proceeding.
				if deleted != (mode == "empty") {
					t.Fatal("network ownership boundary", mode, deleted)
				}
			})
	}
}
