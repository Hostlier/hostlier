package migration

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hostlier/internal/controller"
	"hostlier/internal/model"
	runtimes "hostlier/internal/runtime"
	"hostlier/internal/store"
)

// fakeHost retains the typed inputs required by this migration step.
type fakeHost struct {
	t                                     *testing.T
	o                                     Options
	sourceRunning, targetRunning, guarded bool
	fail                                  string
	events                                []string
}

// event keeps this migration step within its validated recovery boundary.
func (h *fakeHost) event(name string) error {
	h.events = append(h.events, name)
	// Check the observed result against the fixture's migration invariant.
	if h.fail == name {
		return errors.New("injected " + name)
	}
	return nil
}

// service keeps this migration step within its validated recovery boundary.
func (h *fakeHost) service(_ context.Context, action string) error {
	// Check the observed result against the fixture's migration invariant.
	if action == "start" {
		var s controller.State
		st, err := store.Open(h.o.StateDir, &s)
		// Check the observed result against the fixture's migration invariant.
		if err != nil {
			h.t.Fatal("controller started while migration still holds store:", err)
		}
		st.Close()
	}
	return h.event("service-" + action)
}

// guard keeps this migration step within its validated recovery boundary.
func (h *fakeHost) guard(active bool) error {
	h.guarded = active
	return h.event(fmt.Sprintf("guard-%t", active))
}

// unit keeps this migration step within its validated recovery boundary.
func (h *fakeHost) unit() ([]byte, error) { return []byte("managed Docker unit"), nil }

// install keeps this migration step within its validated recovery boundary.
func (h *fakeHost) install(context.Context, *Journal) error { return h.event("install") }

// sourceFence keeps this migration step within its validated recovery boundary.
func (h *fakeHost) sourceFence(_ context.Context, j *Journal, save func() error) error {
	h.sourceRunning = false
	j.Policies["source"] = json.RawMessage(`{"Name":"unless-stopped"}`)
	// Check the observed result against the fixture's migration invariant.
	if err := save(); err != nil {
		return err
	}
	return h.event("source-fence")
}

// targetFence keeps this migration step within its validated recovery boundary.
func (h *fakeHost) targetFence(context.Context, *Journal) error {
	// Check the observed result against the fixture's migration invariant.
	if err := h.event("target-fence"); err != nil {
		return err
	}
	h.targetRunning = false
	return nil
}

// prepareVolumes keeps this migration step within its validated recovery boundary.
func (h *fakeHost) prepareVolumes(ctx context.Context, j *Journal) error {
	// Exercise each fixture case against the same recovery invariant.
	for _, v := range j.Plan.Volumes {
		path := h.targetPath(j, v)
		// Check the observed result against the fixture's migration invariant.
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		// Check the observed result against the fixture's migration invariant.
		if err := fixtureRestore(ctx, j.Backups[v.Name], path, false); err != nil {
			return err
		}
	}
	return h.event("prepare-volumes")
}

// deploy keeps this migration step within its validated recovery boundary.
func (h *fakeHost) deploy(_ context.Context, p model.Project, _ model.DeployContext) (model.Deployment, error) {
	// Check the observed result against the fixture's migration invariant.
	if h.sourceRunning {
		h.t.Fatal("target started before source fencing")
	}
	h.targetRunning = true
	os.WriteFile(filepath.Join(h.o.StateDir, "target", "data"), []byte("target-write"), 0600)
	return model.Deployment{Name: p.Name, RuntimeID: "k3s-app", ImageDigest: p.Image, Status: "running"}, h.event("deploy")
}

// restoreSource keeps this migration step within its validated recovery boundary.
func (h *fakeHost) restoreSource(context.Context, *Journal) error {
	// Check the observed result against the fixture's migration invariant.
	if h.targetRunning {
		h.t.Fatal("two writers started")
	}
	h.sourceRunning = true
	return h.event("restore-source")
}

// setRuntime keeps this migration step within its validated recovery boundary.
func (h *fakeHost) setRuntime(_ context.Context, _ *Journal, backend string) error {
	return h.event("runtime-" + backend)
}

// targetPath keeps this migration step within its validated recovery boundary.
func (h *fakeHost) targetPath(_ *Journal, _ runtimes.MigrationVolume) string {
	return filepath.Join(h.o.StateDir, "target")
}

// fixtureArchive keeps this migration step within its validated recovery boundary.
func fixtureArchive(_ context.Context, source, destination string) (Archive, error) {
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		return Archive{}, err
	}
	writer := tar.NewWriter(f)
	// Supply isolated fixture behavior without contacting a live server.
	err = filepath.WalkDir(source, func(p string, d os.DirEntry, err error) error {
		// Check the observed result against the fixture's migration invariant.
		if err != nil {
			return err
		}
		// Check the observed result against the fixture's migration invariant.
		if d.IsDir() {
			return nil
		}
		name, _ := filepath.Rel(source, p)
		data, err := os.ReadFile(p)
		// Check the observed result against the fixture's migration invariant.
		if err != nil {
			return err
		}
		// Check the observed result against the fixture's migration invariant.
		if err := writer.WriteHeader(&tar.Header{Name: filepath.ToSlash(name), Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	})
	// Check the observed result against the fixture's migration invariant.
	if err == nil {
		err = writer.Close()
	}
	f.Close()
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		return Archive{}, err
	}
	return hashFile(destination)
}

// fixtureRestore keeps this migration step within its validated recovery boundary.
func fixtureRestore(_ context.Context, a Archive, destination string, replace bool) error {
	// Check the observed result against the fixture's migration invariant.
	if err := verifyArchive(a); err != nil {
		return err
	}
	// Check the observed result against the fixture's migration invariant.
	if replace {
		// Check the observed result against the fixture's migration invariant.
		if err := clearFixtureDirectory(destination); err != nil {
			return err
		}
	}
	f, err := os.Open(a.File)
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		return err
	}
	defer f.Close()
	r := tar.NewReader(f)
	// Bound repeated observations by completion, input exhaustion or cancellation.
	for {
		header, err := r.Next()
		// Check the observed result against the fixture's migration invariant.
		if err == io.EOF {
			return nil
		}
		// Check the observed result against the fixture's migration invariant.
		if err != nil {
			return err
		}
		data, err := io.ReadAll(r)
		// Check the observed result against the fixture's migration invariant.
		if err != nil {
			return err
		}
		// Check the observed result against the fixture's migration invariant.
		if err := os.WriteFile(filepath.Join(destination, header.Name), data, 0600); err != nil {
			return err
		}
	}
}

// engineFixture keeps this migration step within its validated recovery boundary.
func engineFixture(t *testing.T) (*engine, *fakeHost, Plan) {
	t.Helper()
	root := t.TempDir()
	o := Options{StateDir: filepath.Join(root, "state"), Config: filepath.Join(root, "config.yaml"), BackupDir: filepath.Join(root, "backups")}
	requireMigration(t, os.Mkdir(o.StateDir, 0700))
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "data"), []byte("original"), 0600)
	os.WriteFile(o.Config, []byte("version: 1"), 0600)
	p := Plan{Options: o, From: "docker", To: "k3s", Applications: []string{"app"}, Fingerprint: "approved", Volumes: []runtimes.MigrationVolume{{Application: "app", Name: "volume", Claim: "data-main", Size: "1Gi", Path: source}}}
	s := controller.State{Version: 2, Runtime: "docker", Applications: map[string]*controller.Application{"app": {Deployment: model.Deployment{Name: "app", RuntimeID: "docker-app", Status: "running"}, Spec: model.Project{Version: 1, Name: "app", Image: "image@sha256:" + strings.Repeat("a", 64)}}}}
	st, err := store.Open(o.StateDir, &s)
	requireMigration(t, err)
	requireMigration(t, st.Save(s))
	st.Close()
	h := &fakeHost{t: t, o: o, sourceRunning: true}
	// Supply isolated fixture behavior without contacting a live server.
	e := &engine{host: h, inspect: func(context.Context, Options, controller.State) (Plan, error) { return p, nil }, backup: func(ctx context.Context, path, file string) (Archive, error) {
		// Check the observed result against the fixture's migration invariant.
		if h.sourceRunning || h.targetRunning {
			t.Fatal("backup with active writer")
		}
		return fixtureArchive(ctx, path, file)
		// Supply isolated fixture behavior without contacting a live server.
	}, restore: func(ctx context.Context, a Archive, path string, replace bool) error {
		// Check the observed result against the fixture's migration invariant.
		if h.sourceRunning || h.targetRunning {
			t.Fatal("copy with active writer")
		}
		return fixtureRestore(ctx, a, path, replace)
		// Supply isolated fixture behavior without contacting a live server.
	}, export: func(s *store.Store, file, config string) error {
		return s.Export(file, config, filepath.Join(root, "settings"), filepath.Join(root, "caddy"))
	}}
	return e, h, p
}

// loadState keeps this migration step within its validated recovery boundary.
func loadState(t *testing.T, dir string) controller.State {
	t.Helper()
	var s controller.State
	requireMigration(t, store.Snapshot(dir, &s))
	return s
}

// TestMigrationCutoverAndRollbackPreserveCurrentData keeps this migration step within its validated recovery boundary.
func TestMigrationCutoverAndRollbackPreserveCurrentData(t *testing.T) {
	e, h, p := engineFixture(t)
	requireMigration(t, e.apply(context.Background(), p))
	j, err := Read(p.StateDir)
	requireMigration(t, err)
	// Check the observed result against the fixture's migration invariant.
	if j.Phase != "completed" {
		t.Fatal(j.Phase)
	}
	assertMigrationWriters(t, h, false)
	assertMigrationRuntime(t, p.StateDir, "k3s", "k3s-app")
	original, _ := os.ReadFile(filepath.Join(p.Volumes[0].Path, "data"))
	// Check the observed result against the fixture's migration invariant.
	if string(original) != "original" {
		t.Fatal("cutover modified original Docker data")
	}
	// New writes after cutover must survive an explicit return to Docker.
	os.WriteFile(filepath.Join(h.targetPath(j, p.Volumes[0]), "data"), []byte("newest-write"), 0600)
	var s controller.State
	st, err := store.Open(p.StateDir, &s)
	requireMigration(t, err)
	requireMigration(t, e.rollback(context.Background(), j, st, &s))
	st.Close()
	data, _ := os.ReadFile(filepath.Join(p.Volumes[0].Path, "data"))
	// Check the observed result against the fixture's migration invariant.
	if string(data) != "newest-write" {
		t.Fatal(string(data))
	}
	assertMigrationWriters(t, h, true)
	assertMigrationRuntime(t, p.StateDir, "docker", "docker-app")
	requireMigration(t, verifyArchive(j.Backups["volume"]))
	requireMigration(t, verifyArchive(j.TargetBackups["volume"]))
}

// TestMigrationFailureRollsBackAtEffectBoundaries keeps this migration step within its validated recovery boundary.
func TestMigrationFailureRollsBackAtEffectBoundaries(t *testing.T) {
	// Exercise each fixture case against the same recovery invariant.
	for _, failure := range []string{"install", "prepare-volumes", "deploy", "runtime-k3s"} {
		// Supply isolated fixture behavior without contacting a live server.
		t.Run(failure, func(t *testing.T) {
			e, h, p := engineFixture(t)
			h.fail = failure
			// Check the observed result against the fixture's migration invariant.
			if err := e.apply(context.Background(), p); err == nil {
				t.Fatal("injected failure succeeded")
			}
			j, err := Read(p.StateDir)
			requireMigration(t, err)
			// Check the observed result against the fixture's migration invariant.
			if j.Phase != "rolled-back" || !h.sourceRunning || h.targetRunning || h.guarded {
				t.Fatal(j.Phase, h.events)
			}
			data, _ := os.ReadFile(filepath.Join(p.Volumes[0].Path, "data"))
			want := "original"
			// Check the observed result against the fixture's migration invariant.
			if failure == "deploy" || failure == "runtime-k3s" {
				want = "target-write"
			}
			// Check the observed result against the fixture's migration invariant.
			if string(data) != want {
				t.Fatal("rollback lost data", string(data), want)
			}
		})
	}
}

// TestUncertainTargetFenceNeverStartsSource keeps this migration step within its validated recovery boundary.
func TestUncertainTargetFenceNeverStartsSource(t *testing.T) {
	e, h, p := engineFixture(t)
	requireMigration(t, e.apply(context.Background(), p))
	j, _ := Read(p.StateDir)
	h.fail = "target-fence"
	h.guarded = true
	var s controller.State
	st, err := store.Open(p.StateDir, &s)
	requireMigration(t, err)
	defer st.Close()
	// Check the observed result against the fixture's migration invariant.
	if err := e.rollback(context.Background(), j, st, &s); err == nil {
		t.Fatal("fence uncertainty ignored")
	}
	// Check the observed result against the fixture's migration invariant.
	if h.sourceRunning || !h.guarded {
		t.Fatal("source started despite unverified target shutdown")
	}
}

// TestCrashRecoveryUsesJournalAndRetainsGuard keeps this migration step within its validated recovery boundary.
func TestCrashRecoveryUsesJournalAndRetainsGuard(t *testing.T) {
	// Exercise each fixture case against the same recovery invariant.
	for _, phase := range []string{"backing-up", "backed-up", "restoring", "committing"} {
		// Supply isolated fixture behavior without contacting a live server.
		t.Run(phase, func(t *testing.T) {
			e, h, p := engineFixture(t)
			// Supply isolated fixture behavior without contacting a live server.
			e.progress = func(current string) {
				// Check the observed result against the fixture's migration invariant.
				if current == phase {
					panic("simulated process death")
				}
			}
			// Supply isolated fixture behavior without contacting a live server.
			func() {
				// Supply isolated fixture behavior without contacting a live server.
				defer func() {
					// Check the observed result against the fixture's migration invariant.
					if recover() == nil {
						t.Fatal("crash was not reached")
					}
				}()
				_ = e.apply(context.Background(), p)
			}()
			j, err := Read(p.StateDir)
			requireMigration(t, err)
			// Check the observed result against the fixture's migration invariant.
			if !h.guarded {
				t.Fatal("crash removed guard")
			}
			e.progress = nil
			var state controller.State
			st, err := store.Open(p.StateDir, &state)
			requireMigration(t, err)
			requireMigration(t, e.rollback(context.Background(), j, st, &state))
			st.Close()
			// Check the observed result against the fixture's migration invariant.
			if !h.sourceRunning || h.targetRunning || h.guarded {
				t.Fatal(h.events)
			}
		})
	}
}

// TestRollbackRejectsChangedSecretsAndSpecifications keeps this migration step within its validated recovery boundary.
func TestRollbackRejectsChangedSecretsAndSpecifications(t *testing.T) {
	e, _, p := engineFixture(t)
	requireMigration(t, e.apply(context.Background(), p))
	j, _ := Read(p.StateDir)
	s := loadState(t, p.StateDir)
	s.Applications["app"].SecretRefs = map[string]controller.SecretRef{"KEY": {Name: "rotated", Revision: "two"}}
	// Check the observed result against the fixture's migration invariant.
	if err := rollbackCompatible(j, s); err == nil {
		t.Fatal("changed secret revision accepted")
	}
}

// TestPrivateMigrationOrderAndCycles keeps this migration step within its validated recovery boundary.
func TestPrivateMigrationOrderAndCycles(t *testing.T) {
	apps := map[string]*controller.Application{"web": {Spec: model.Project{Bindings: map[string]string{"DB": "applications.db.endpoint"}}}, "db": {Spec: model.Project{}}}
	order, err := applicationOrder(apps)
	// Check the observed result against the fixture's migration invariant.
	if err != nil || !reflect.DeepEqual(order, []string{"db", "web"}) {
		t.Fatal(order, err)
	}
	apps["db"].Spec.Bindings = map[string]string{"BACK": "applications.web.endpoint"}
	// Check the observed result against the fixture's migration invariant.
	if _, err := applicationOrder(apps); err == nil {
		t.Fatal("cycle accepted")
	}
}

// requireMigration keeps setup failures separate from the behavioral assertions.
func requireMigration(t *testing.T, err error) {
	t.Helper()
	// Check the observed result against the fixture's migration invariant.
	if err != nil {
		t.Fatal(err)
	}
}

// clearFixtureDirectory simulates a fenced destructive copy without touching another fixture.
func clearFixtureDirectory(destination string) error {
	entries, _ := os.ReadDir(destination)
	// Exercise each fixture case against the same recovery invariant.
	for _, entry := range entries {
		// Check the observed result against the fixture's migration invariant.
		if err := os.RemoveAll(filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// assertMigrationRuntime checks the committed API identity after each handoff.
func assertMigrationRuntime(t *testing.T, dir, backend, id string) {
	t.Helper()
	s := loadState(t, dir)
	// Check the observed result against the fixture's migration invariant.
	if s.Runtime != backend || s.Applications["app"].RuntimeID != id {
		t.Fatal(s)
	}
}

// assertMigrationWriters independently enforces the single-writer and guard invariants.
func assertMigrationWriters(t *testing.T, h *fakeHost, source bool) {
	t.Helper()
	// Check the observed result against the fixture's migration invariant.
	if h.sourceRunning != source || h.targetRunning == source || h.guarded {
		t.Fatal(h.events)
	}
}
