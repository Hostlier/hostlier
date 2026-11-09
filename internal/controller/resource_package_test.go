package controller

import (
	"context"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// TestExistingDependencyRefreshesPackageApproval covers migration and approved executable upgrades.
func TestExistingDependencyRefreshesPackageApproval(t *testing.T) {
	manager := &recordingResources{digest: strings.Repeat("a", 64)}
	c := resourceController(t, t.TempDir(), &recordingRuntime{}, manager)
	defer closeController(t, c)
	spec := model.ResourceSpec{Name: "shared", Type: "postgres", Storage: "1Gi", Image: "postgres@sha256:" + strings.Repeat("b", 64)}
	c.state.Resources[spec.Name] = &ResourceRecord{Resource: model.Resource{Name: spec.Name, Type: spec.Type, Status: "running"}, Spec: spec}
	// A legacy resource has no package digest, but fresh review must pin the installed package.
	for _, digest := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		manager.digest = digest
		planned, err := c.prepareDependency(context.Background(), "app", "db", model.Dependency{Source: "existing", Instance: spec.Name, Type: spec.Type})
		// An executable upgrade does not change storage, image or resource ownership.
		if err != nil || planned.Spec.PackageDigest != digest {
			t.Fatal("existing dependency did not refresh package approval", err)
		}
		c.state.Resources[spec.Name].Spec = planned.Spec
	}
	spec.Storage = "2Gi"
	// Ignoring package metadata must not permit unapproved immutable resource changes.
	if _, err := c.prepareResource(context.Background(), spec); err == nil {
		t.Fatal("storage change accepted as a package upgrade")
	}
}

// TestPackagePreflightKeepsRetainedResourceHealthy avoids poisoning inventory before package execution.
func TestPackagePreflightKeepsRetainedResourceHealthy(t *testing.T) {
	manager := &recordingResources{fail: true, failCode: "RESOURCE_PACKAGE_PREFLIGHT"}
	c := resourceController(t, t.TempDir(), &recordingRuntime{}, manager)
	defer closeController(t, c)
	spec := model.ResourceSpec{Name: "shared", Type: "postgres", Storage: "1Gi"}
	c.state.Resources[spec.Name] = &ResourceRecord{Resource: model.Resource{Name: spec.Name, Type: spec.Type, Status: "running"}, Spec: spec}
	_, _, err := c.provisionResource(context.Background(), spec, "preflight", nil)
	// Failure before a child process starts cannot change a retained resource's observed health.
	if err == nil || c.state.Resources[spec.Name].Status != "running" {
		t.Fatal("preflight changed resource health")
	}
	c.failOperation(&Operation{Kind: "resource", Name: spec.Name}, err)
	// The operation can fail while its already-running resource remains available.
	if c.state.Resources[spec.Name].Status != "running" {
		t.Fatal("terminal preflight changed resource health")
	}
	manager.failCode = "RESOURCE_PACKAGE"
	_, _, err = c.provisionResource(context.Background(), spec, "process", nil)
	// Once execution has started, uncertain failures retain the conservative failure marker.
	if err == nil || c.state.Resources[spec.Name].Status != "failed" {
		t.Fatal("process failure lost uncertain status")
	}
}
