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
	// Local builds and archives defer platform selection until controller discovery.
	for _, source := range []model.ContainerSource{{Context: "."}, {Archive: "app.tar"}} {
		p.Source.Container = &source
		if err := ValidateInput(p); err != nil { // Offline structural validation does not require a controller.
			t.Fatal(err)
		}
	}
}

// TestContainerSourceRejectsAmbiguity fails before a Docker build can execute.
func TestContainerSourceRejectsAmbiguity(t *testing.T) {
	cases := []model.Source{
		{Container: &model.ContainerSource{Architecture: "amd64"}},
		{Container: &model.ContainerSource{Context: ".", Archive: "a.tar", Architecture: "amd64"}},
		{Image: "nginx:latest", Container: &model.ContainerSource{Context: ".", Architecture: "amd64"}},
		{Container: &model.ContainerSource{Context: ".", Architecture: "unknown"}},
		{Container: &model.ContainerSource{Archive: "a.tar", Dockerfile: "Dockerfile", Architecture: "amd64"}},
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

// TestRemovedContainerImageField rejects obsolete declarations instead of ignoring them beside a local input.
func TestRemovedContainerImageField(t *testing.T) {
	for _, document := range []string{ // JSON and YAML both use the same strict project decoder.
		"version: 2\nsource:\n  container:\n    image: ghcr.io/team/private:latest\n",
		"version: 2\nsource:\n  container:\n    context: .\n    image: ghcr.io/team/private:latest\n",
		`{"version":2,"source":{"container":{"archive":"image.tar","image":"ghcr.io/team/private:latest"}}}`,
	} {
		var project model.Project
		if err := DecodeYAML([]byte(document), &project); err == nil { // Removing the field must fail before any runtime source selection.
			t.Fatal("removed source.container.image field accepted")
		}
	}
}

// TestRegistryImageValidation keeps download request validation separate from the local container schema.
func TestRegistryImageValidation(t *testing.T) {
	for _, image := range []string{"ghcr.io/team/private:latest", "nginx:alpine"} { // Both explicit registries and Docker Hub shorthand remain valid.
		if err := ValidateRegistryImage(image, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, image := range []string{"", "invalid image", "https://alice:secret@ghcr.io/team/private:latest"} { // Invalid or credential-bearing references never reach registry lookup.
		if err := ValidateRegistryImage(image, "amd64"); err == nil {
			t.Fatal("invalid registry reference accepted")
		}
	}
	if err := ValidateRegistryImage("nginx:alpine", "unknown"); err == nil { // Download requests cannot bypass supported target checks.
		t.Fatal("unsupported architecture accepted")
	}
}
