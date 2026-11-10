package cli

import (
	"bytes"
	"gopkg.in/yaml.v3"
	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/pkg/product"
	"io"
	"os"
)

// readProductProject expands a bounded local document without provisioning or executing author code.
func readProductProject(path string) (model.Project, error) {
	file, err := os.Open(path)
	// Local I/O errors preserve their actionable file context.
	if err != nil {
		return model.Project{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1048577))
	// Bound product declarations before YAML allocation.
	if err != nil || len(data) > 1048576 {
		return model.Project{}, model.Err("INVALID_CONFIG", "Product file is too large or unreadable")
	}
	return decodeProductProject(data)
}

// decodeProductProject rejects unknown fields and converts only explicit declarative composition.
func decodeProductProject(data []byte) (model.Project, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var document productProject
	// A product file is exactly one versioned YAML document.
	if decoder.Decode(&document) != nil || decoder.Decode(new(any)) != io.EOF || document.Version != 1 {
		return model.Project{}, model.Err("INVALID_CONFIG", "Invalid product declaration")
	}
	// Avoid merging two independent graphs with ambiguous step and binding ownership.
	if document.App.Workflow != nil {
		return model.Project{}, model.Err("INVALID_CONFIG", "Product app must not declare a second workflow")
	}
	composed, err := product.Compose(document.Product, product.Invocation{Inputs: document.Inputs, SecretRefs: document.SecretRefs})
	// Composition validates public input schemas and the whole dependency graph before review.
	if err != nil {
		return model.Project{}, model.Err("INVALID_CONFIG", err.Error())
	}
	document.App.Workflow = &composed
	return config.ValidateProject(document.App)
}

// product expands local package composition and uses the ordinary deployment review and approval path.
func (s *session) product() error {
	// Product operations have the same explicit plan/deploy distinction as ordinary projects.
	if s.sub != "plan" && s.sub != "deploy" {
		return model.Err("ARGUMENTS", "Usage: product plan|deploy --file PATH")
	}
	spec, err := readProductProject(s.projectPath())
	// Invalid declarations must fail before contacting an executable package.
	if err != nil {
		return err
	}
	spec, err = s.prepareDeploymentProject(spec)
	// Product applications use the same registry preparation and local fallback as ordinary deployments.
	if err != nil {
		return err
	}
	spec, source, err := s.deploymentRoute(spec)
	// Route validation occurs before controller planning and approval.
	if err != nil {
		return err
	}
	return s.reviewPlan(planFlow{path: "/v1/plans", body: map[string]any{"spec": spec}, routeSource: source, readOnly: s.sub == "plan", prompt: "Approve this exact product deployment plan? [y/N] ", staleMessage: "Server state changed. Review and approve the updated product plan."})
}
