package config

import (
	"hostlier/internal/model"
	"testing"
)

// TestContainerSourcePreflight preserves authored paths and accepts both explicit local input modes.
func TestContainerSourcePreflight(t *testing.T) {
	p := workloadFixture()
	p.Source = &model.Source{Container: &model.ContainerSource{Context: "dist/app", Architecture: "amd64"}}
	// Structural preflight does not execute Docker or require local paths to exist.
	if err := ValidateInput(p); err != nil {
		t.Fatal(err)
	}
	// Placeholder normalization happens only on a deep copy.
	if p.Source.Container == nil || p.Source.Container.Context != "dist/app" {
		t.Fatal("mutated input")
	}
	// Controllers reject unprepared local paths instead of interpreting them on the server.
	if _, err := ValidateProject(p); err == nil {
		t.Fatal("unprepared server source accepted")
	}
	p.Source.Container = &model.ContainerSource{Archive: "app.tar", Architecture: "arm64"}
	// Archives require no build context.
	if err := ValidateInput(p); err != nil {
		t.Fatal(err)
	}
}

// TestContainerSourceRejectsAmbiguity fails before a Docker build can execute.
func TestContainerSourceRejectsAmbiguity(t *testing.T) {
	cases := []model.Source{
		{Container: &model.ContainerSource{Architecture: "amd64"}},
		{Container: &model.ContainerSource{Context: ".", Archive: "a.tar", Architecture: "amd64"}},
		{Image: "nginx:latest", Container: &model.ContainerSource{Context: ".", Architecture: "amd64"}},
		{Container: &model.ContainerSource{Context: ".", Architecture: "unknown"}},
	}
	// Each source must fail independently without relying on filesystem checks.
	for _, source := range cases {
		p := workloadFixture()
		p.Source = &source
		// No invalid combination may be lowered into a valid placeholder.
		if err := ValidateInput(p); err == nil {
			t.Fatalf("accepted %+v", source)
		}
	}
}
