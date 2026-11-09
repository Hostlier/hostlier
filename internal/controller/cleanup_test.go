package controller

import (
	"context"
	"errors"
	"hostlier/internal/artifact"
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCleanupReferenceProtection covers active images, rollback inputs and accepted work independently of receipt age.
func TestCleanupReferenceProtection(t *testing.T) {
	now := time.Now()
	c := Controller{opts: Options{Now:
	// Supply controlled state for this cleanup boundary.
	func() time.Time { return now }}, state: State{
		Applications: map[string]*Application{"app": {Spec: model.Project{Image: "live", Sidecars: map[string]model.Project{"worker": {Source: &model.Source{Image: "sidecar"}}}}, Revisions: []Revision{{Spec: model.Project{Image: "rollback"}}}}},
		Plans:        map[string]*Plan{"valid": {Spec: model.Project{Image: "planned"}, ExpiresAt: now.Add(time.Minute).UnixMilli()}, "expired": {Spec: model.Project{Image: "expired"}, ExpiresAt: now.Add(-time.Minute).UnixMilli()}},
		Operations:   map[string]*Operation{"running": {Spec: model.Project{Image: "running"}, Status: "running"}, "interrupted": {Spec: model.Project{Image: "recovery"}, Status: "interrupted"}, "failed": {Spec: model.Project{Image: "failed"}, Status: "failed"}, "done": {Spec: model.Project{Image: "done"}, Status: "succeeded"}},
	}}
	keep := c.protectedArtifacts()
	// Check the fixture or cleanup invariant before proceeding.
	for _, image := range []string{"live", "sidecar", "rollback", "planned", "running", "recovery"} {
		// Check the fixture or cleanup invariant before proceeding.
		if !keep[image] {
			t.Fatal("required artifact not protected", image)
		}
	}
	// Check the fixture or cleanup invariant before proceeding.
	for _, image := range []string{"expired", "failed", "done"} {
		// Check the fixture or cleanup invariant before proceeding.
		if keep[image] {
			t.Fatal("terminal input retained forever", image)
		}
	}
}

func TestDeletedAppReleasesImageProtection(t *testing.T) {
	c := Controller{opts: Options{Now: time.Now}, state: State{Applications: map[string]*Application{
		"deleted": {Deployment: model.Deployment{Name: "deleted", Status: "deleted"}, Spec: model.Project{Image: "old"}, Revisions: []Revision{{Spec: model.Project{Image: "rollback"}}}},
		"stopped": {Deployment: model.Deployment{Name: "stopped", Status: "stopped"}, Spec: model.Project{Image: "needed"}},
	}}}
	keep := c.protectedArtifacts()
	if keep["old"] || keep["rollback"] || !keep["needed"] {
		t.Fatal("deleted and stopped image protection", keep)
	}
}

// failingArtifactCleaner models a daemon outage without invoking a host-wide prune.
type failingArtifactCleaner struct {
	fail   bool
	images []string
}

// RemoveArtifact records exact cache identities and can fail before confirming removal.
func (f *failingArtifactCleaner) RemoveArtifact(ctx context.Context, image string) error {
	f.images = append(f.images, image)
	// Check the fixture or cleanup invariant before proceeding.
	if f.fail {
		return errors.New("daemon unavailable")
	}
	return nil
}

// TestImageCleanupRetriesAndProtectsReuse keeps eviction intent across failures but cancels it for reused images.
func TestImageCleanupRetriesAndProtectsReuse(t *testing.T) {
	s := artifact.Store{Root: t.TempDir()}
	id := strings.Repeat("a", 64)
	image := "hostlier.local/artifacts@sha256:" + id
	path := filepath.Join(s.Root, id+".gc")
	// Check the fixture or cleanup invariant before proceeding.
	if err := os.WriteFile(path, []byte(image), 0600); err != nil {
		t.Fatal(err)
	}
	f := &failingArtifactCleaner{fail: true}
	// Check the fixture or cleanup invariant before proceeding.
	if err := cleanupImages(context.Background(), s, f, nil); err == nil {
		t.Fatal("daemon failure hidden")
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := os.Stat(path); err != nil {
		t.Fatal("retry marker lost", err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if err := cleanupImages(context.Background(), s, f, map[string]bool{image: true}); err != nil {
		t.Fatal(err)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if len(f.images) != 1 {
		t.Fatal("protected image evicted", f.images)
	}
	// Check the fixture or cleanup invariant before proceeding.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("superseded eviction retained", err)
	}
}
