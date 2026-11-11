package packagekit

import (
	"encoding/json"
	"hostlier/internal/model"
	"hostlier/pkg/workflow"
)

// ProjectPlanRequest asks the local package to read the current project without building it.
type ProjectPlanRequest struct {
	Config           map[string]any `json:"config,omitempty"`
	ProvidedBindings []string       `json:"providedBindings,omitempty"`
	Version          int            `json:"version"`
	ProjectDir       string         `json:"projectDir"`
	Architecture     string         `json:"architecture"`
}

// ProjectPlanResponse contains the public spec and any package-specific declarations to carry forward.
type ProjectPlanResponse struct {
	Version      int             `json:"version"`
	Project      json.RawMessage `json:"project,omitempty"`
	Declarations json.RawMessage `json:"declarations,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// ProjectPackageRequest carries the exact public spec and optional declarations chosen for this build.
type ProjectPackageRequest struct {
	Config           map[string]any  `json:"config,omitempty"`
	ProvidedBindings []string        `json:"providedBindings,omitempty"`
	Version          int             `json:"version"`
	ProjectDir       string          `json:"projectDir"`
	OutputDir        string          `json:"outputDir"`
	Architecture     string          `json:"architecture"`
	Project          json.RawMessage `json:"project"`
	Declarations     json.RawMessage `json:"declarations,omitempty"`
}

// ProjectArtifact identifies the generated context beneath the output directory.
type ProjectArtifact struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// ProjectPackageResponse returns either an artifact descriptor or a public error.
type ProjectPackageResponse struct {
	Version  int             `json:"version"`
	Artifact ProjectArtifact `json:"artifact,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// ProjectContribution is an additive proposal from one explicitly enrolled local package.
// Controller-side effects are represented by ordinary workflow steps.
type ProjectContribution struct {
	Version          int                          `json:"version"`
	Sidecars         map[string]ProjectSidecar    `json:"sidecars,omitempty"`
	Secrets          map[string]string            `json:"secrets,omitempty"`
	Dependencies     map[string]ProjectDependency `json:"dependencies,omitempty"`
	Bindings         map[string]string            `json:"bindings,omitempty"`
	Steps            []workflow.Step              `json:"steps,omitempty"`
	WorkflowBindings map[string]string            `json:"workflowBindings,omitempty"`
	Error            string                       `json:"error,omitempty"`
}

// ProjectDependency requests a named native Hostlier resource.
type ProjectDependency = model.Dependency

// ProjectSidecar uses the ordinary workload contract, including scoped secrets and health probes.
// Composition validates it as a sidecar; it cannot create a nested or public deployment.
type ProjectSidecar = model.Project
