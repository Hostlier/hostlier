package runtime

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
)

// migrationInventoryFixture uses real runtime metadata with an isolated daemon transport.
func migrationInventoryFixture(t *testing.T, foreignWriter bool) *Runtime {
	t.Helper()
	r := &Runtime{owner: "fixture", options: Options{StateDir: t.TempDir()}}
	app := appRecord{Deployment: model.Deployment{Name: "app", RuntimeID: "primary", Status: "running"}, Spec: model.Project{Version: 2, Name: "app", Volumes: map[string]model.Volume{"main": {Size: "1Gi"}}, Sidecars: map[string]model.Project{"cache": {Volumes: map[string]model.Volume{"data": {Size: "2Gi"}}}}}, Members: map[string]string{"cache": "sidecar"}}
	// Check the observed result against the fixture's migration invariant.
	if err := r.save(state{Apps: map[string]appRecord{"app": app}, Retained: map[string]appRecord{}}); err != nil {
		t.Fatal(err)
	}
	// Supply isolated fixture behavior without contacting a live server.
	r.docker = &http.Client{Transport: sidecarTransport(func(req *http.Request) (*http.Response, error) {
		// Select only representations explicitly supported by the migration contract.
		switch {
		// Keep this alternative within the validated operation or archive contract.
		case strings.HasPrefix(req.URL.Path, "/volumes/"):
			name := strings.TrimPrefix(req.URL.Path, "/volumes/")
			return sidecarResponse(map[string]any{"Name": name, "Driver": "local", "Mountpoint": "/var/lib/docker/volumes/" + name + "/_data", "Labels": r.labels("app")})
		// Keep this alternative within the validated operation or archive contract.
		case req.URL.Path == "/containers/json":
			labels := r.labels("app")
			// Check the observed result against the fixture's migration invariant.
			if foreignWriter {
				labels = map[string]string{"external": "true"}
			}
			return sidecarResponse([]any{map[string]any{"Labels": labels, "Mounts": []any{map[string]string{"Name": "hostlier-fixture-app-main", "Type": "volume"}}}})
		// Keep this alternative within the validated operation or archive contract.
		case strings.HasPrefix(req.URL.Path, "/containers/"):
			return sidecarResponse(map[string]any{"Config": map[string]any{"Labels": r.labels("app")}})
		// Keep this alternative within the validated operation or archive contract.
		default:
			return nil, fmt.Errorf("unexpected migration fixture request")
		}
	})}
	return r
}

// migrationStartupFixture observes the writer handoff through the real Docker adapter.
type migrationStartupFixture struct {
	labels     map[string]string
	running    map[string]bool
	events     []string
	probeFails bool
}

// request emulates only lifecycle and exec-probe calls needed by source restoration.
func (f *migrationStartupFixture) request(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	// Select only representations explicitly supported by the migration contract.
	switch {
	// Keep this alternative within the validated operation or archive contract.
	case req.Method == "GET" && strings.HasPrefix(path, "/containers/"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		return sidecarResponse(map[string]any{"Config": map[string]any{"Labels": f.labels}, "State": map[string]bool{"Running": f.running[id]}})
	// Keep this alternative within the validated operation or archive contract.
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start")
		f.running[id] = true
		f.events = append(f.events, "start:"+id)
		return sidecarResponse(map[string]any{})
	// Keep this alternative within the validated operation or archive contract.
	case path == "/containers/cache/exec":
		f.events = append(f.events, "probe:cache")
		return sidecarResponse(map[string]string{"Id": "probe"})
	// Keep this alternative within the validated operation or archive contract.
	case path == "/exec/probe/start":
		return sidecarResponse(map[string]any{})
	// Keep this alternative within the validated operation or archive contract.
	case path == "/exec/probe/json":
		// Check the observed result against the fixture's migration invariant.
		code := 0
		// Check the observed result against the fixture's migration invariant.
		if f.probeFails {
			code = 1
		}
		return sidecarResponse(map[string]any{"Running": false, "ExitCode": code})
	// Keep this alternative within the validated operation or archive contract.
	default:
		return nil, fmt.Errorf("unexpected startup fixture call %s", path)
	}
}

// TestMigrationRollbackWaitsForRequiredSidecar prevents Margin's init-bucket
// command from starting before its retained storage sidecar is ready.
func TestMigrationRollbackWaitsForRequiredSidecar(t *testing.T) {
	// Exercise each fixture case against the same recovery invariant.
	for _, fails := range []bool{false, true} {
		r := &Runtime{owner: "fixture", options: Options{HealthTimeout: 10 * time.Millisecond}}
		fixture := &migrationStartupFixture{labels: r.labels("app"), running: map[string]bool{}, probeFails: fails}
		r.docker = &http.Client{Transport: sidecarTransport(fixture.request)}
		app := appRecord{Deployment: model.Deployment{Name: "app", RuntimeID: "primary"}, Spec: model.Project{Version: 2, Name: "app", Sidecars: map[string]model.Project{"cache": {Health: &model.Health{Exec: []string{"probe"}}}}}, Members: map[string]string{"cache": "cache"}}
		err := r.startMigrationGroup(t.Context(), app)
		// Check the observed result against the fixture's migration invariant.
		if (err != nil) != fails {
			t.Fatal("sidecar probe result ignored", err)
		}
		primary := strings.Contains(strings.Join(fixture.events, ","), "start:primary")
		// Check the observed result against the fixture's migration invariant.
		if primary == fails {
			t.Fatal("primary writer started without successful required-sidecar readiness", fixture.events)
		}
	}
}

// TestMigrationInventoryIncludesSidecarsAndRejectsForeignWriters checks data ownership boundaries.
func TestMigrationInventoryIncludesSidecarsAndRejectsForeignWriters(t *testing.T) {
	r := migrationInventoryFixture(t, false)
	inv, err := r.MigrationInventory(context.Background())
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
	// Check the observed result against the fixture's migration invariant.
	if len(inv.Volumes) != 2 {
		t.Fatal(inv.Volumes)
	}
	claims := map[string]bool{}
	// Exercise each fixture case against the same recovery invariant.
	for _, v := range inv.Volumes {
		claims[v.Claim] = true
	}
	// Check the observed result against the fixture's migration invariant.
	if !claims["data-main"] || !claims["sidecar-cache-data"] {
		t.Fatal(claims)
	}
	r = migrationInventoryFixture(t, true)
	// Check the observed result against the fixture's migration invariant.
	if _, err := r.MigrationInventory(context.Background()); err == nil {
		t.Fatal("unmanaged volume writer accepted")
	}
}
