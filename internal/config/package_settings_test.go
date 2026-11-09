package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProjectPackageSettings checks that several local namespaces coexist without entering controller plans.
func TestProjectPackageSettings(t *testing.T) {
	path := secretSourceFile(t, "name: demo\npackage: harnest\npackages:\n  harnest: {artifact: local-builder-input}\n  companion: {required: true}\n")
	input, err := ReadProjectSources(path, "")
	// Multiple independently configured namespaces must decode in the same manifest.
	if err != nil {
		t.Fatal(err)
	}
	// Local settings and the legacy builder selection can coexist.
	if input.Project.Package != "harnest" || input.Project.Packages["harnest"]["artifact"] != "local-builder-input" || input.Project.Packages["companion"]["required"] != true {
		t.Fatal("lost local package settings")
	}
	wire, err := json.Marshal(input.Project)
	// The controller receives only the deployable workload.
	if err != nil || strings.Contains(string(wire), "local-builder-input") || strings.Contains(string(wire), "packages") {
		t.Fatal("local settings reached the controller project")
	}
}
