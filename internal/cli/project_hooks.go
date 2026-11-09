package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/model"
	"hostlier/internal/packageexec"
	"hostlier/pkg/packagekit"
)

// projectHookCommand restricts source selection to the local plan and deployment entry points.
func projectHookCommand(command string) bool {
	return command == "plan" || command == "deploy" || command == "update"
}

// localProjectPlan contains the public proposal shown locally and handed to the packager.
type localProjectPlan struct {
	// original captures the proposal before route and target overrides.
	original      *packagekit.ProjectPlanResponse
	buildProject  json.RawMessage
	enrolled      *enrolledPackages
	Contributions map[string]packagekit.ProjectContribution `json:"contributions,omitempty"`
	Package       string                                    `json:"package"`
	Architecture  string                                    `json:"architecture"`
	Project       model.Project                             `json:"project"`
	Declarations  json.RawMessage                           `json:"declarations,omitempty"`
}

// runSelectedPackage operates on a pinned local package, without requiring init or a controller for plan.
func (s *session) runSelectedPackage() (bool, error) {
	selected, authored, err := s.projectHookSelection()
	// An unselected project continues through the ordinary CLI workflow.
	if err != nil || (selected == "" && (authored == nil || len(authored.Project.Packages) == 0)) {
		return false, err
	}
	// Reject a blanket approval before invoking any source tooling.
	if s.flags["yes"] != "" {
		return true, model.Err("APPROVAL_REQUIRED", "--yes does not authorize a package deployment")
	}
	group, err := s.loadEnrollment(selected, authored)
	// Do not fall back to an ordinary deploy after a selected package fails.
	if err != nil {
		return true, err
	}
	defer group.close()
	return true, s.runProjectHooks(group, authored)
}

// runProjectHooks previews locally unless planning for a selected controller.
func (s *session) runProjectHooks(group *enrolledPackages, authored *config.ProjectInput) error {
	arch, err := s.projectArchitecture(authored)
	// A failed receiver lookup cannot select a local build platform.
	if err != nil {
		return err
	}
	// Linux architectures are explicit, independent of the laptop that runs this command.
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("Target architecture must be amd64 or arm64")
	}
	// All contributions must be available before validating the builder’s requirements.
	if err := s.collect(group, arch, false); err != nil {
		return err
	}
	hooks := group.hooks[group.builder]
	// An image-only project can have contributors without a source compiler.
	if hooks != nil {
		hooks.SetProvidedBindings(group.provided())
	}
	plan, err := s.planPackage(hooks, group, arch, authored)
	// Invalid declarations stop before any deployment action.
	if err != nil {
		return err
	}
	// Planning ends before packaging or upload.
	if s.command == "plan" {
		return s.print(plan)
	}
	return s.authRecoveryHint(s.deployPackage(hooks, plan, authored))
}

// projectHookSelection keeps explicit artifact files independent of source-directory package discovery.
func (s *session) projectHookSelection() (string, *config.ProjectInput, error) {
	name, explicit := s.flags["package"]
	// Bound package identities before consulting local installation directories.
	if explicit && !packageexec.ValidName(name) {
		return "", nil, fmt.Errorf("Invalid package name")
	}
	// An explicit artifact file bypasses implicit source preparation.
	if s.flags["file"] != "" {
		// Do not silently combine a prepared file with package-owned inputs.
		if explicit {
			return "", nil, fmt.Errorf("Choose --package for a source project or --file for a prepared deployment")
		}
		return "", nil, nil
	}
	input, err := s.optionalPackageProject()
	// An explicit package can operate on an existing project without init.
	if err != nil || input == nil {
		return name, nil, err
	}
	// A saved selection enables later invocations without repeating the flag.
	if !explicit {
		return input.Project.Package, input, nil
	}
	// A different saved owner requires an explicit change to the project.
	if input.Project.Package != "" && name != input.Project.Package {
		return "", nil, fmt.Errorf("Selected package %q differs from package %q in .hostlier", name, input.Project.Package)
	}
	return name, input, nil
}

// optionalPackageProject reads bindings without resolving secret values for a local preview.
func (s *session) optionalPackageProject() (*config.ProjectInput, error) {
	// First-run projects can exist before any Hostlier deployment file.
	if _, err := os.Lstat(s.projectPath()); os.IsNotExist(err) {
		return nil, nil
		// Distinguish an unreadable deployment document from an absent one.
	} else if err != nil {
		return nil, err
	}
	input, err := config.ReadProjectSources(s.projectPath(), "")
	return &input, err
}

// projectArchitecture uses the receiving controller when an operation targets a server.
func (s *session) projectArchitecture(authored *config.ProjectInput) (string, error) {
	arch := s.requestedArchitecture(authored)
	// A standalone preview has no controller to query.
	if s.command == "plan" && s.flags["server"] == "" && s.flags["socket"] == "" && s.flags["on"] == "" {
		return defaultValue(arch, "amd64"), nil
	}
	target, err := s.availablePackagePlatform()
	// Failure to discover the receiver must stop the build.
	if err != nil {
		return "", err
	}
	// A saved or explicit constraint must agree with the serving controller.
	if target.OS != "linux" || (arch != "" && arch != target.Arch) {
		return "", fmt.Errorf("Controller targets %s/%s; requested Linux architecture %q", target.OS, target.Arch, arch)
	}
	return target.Arch, nil
}

// requestedArchitecture preserves an explicit CLI or project constraint for server validation.
func (s *session) requestedArchitecture(authored *config.ProjectInput) string {
	// The invocation can choose a target when using one portable manifest.
	if arch := s.flags["architecture"]; arch != "" {
		return arch
	}
	// Only authored container sources carry a target constraint.
	if authored != nil && authored.Project.Source != nil && authored.Project.Source.Container != nil {
		return authored.Project.Source.Container.Architecture
	}
	return ""
}

// planPackage accepts only an ordinary typed project. Packages never get controller credentials.
func (s *session) planPackage(hooks *packageexec.ProjectHooks, group *enrolledPackages, arch string, authored *config.ProjectInput) (localProjectPlan, error) {
	name := group.builder
	plan := localProjectPlan{Package: name, Architecture: arch, enrolled: group, Contributions: group.contributions}
	// A project planner can derive the whole spec from the package’s own files.
	if hooks.Has("project-plan") {
		response, err := hooks.Plan(s.ctx, arch)
		// Stop on protocol errors or public hook diagnostics.
		if err != nil {
			return plan, err
		}
		decoder := json.NewDecoder(bytes.NewReader(response.Project))
		decoder.DisallowUnknownFields()
		// Unknown Hostlier fields must never be silently dropped from a review.
		if err = decoder.Decode(&plan.Project); err != nil {
			return plan, fmt.Errorf("Package %q returned an invalid Hostlier project", name)
		}
		plan.Declarations = response.Declarations
		plan.original = &response
		// A package that omits planning can consume an ordinary authored deployment.
	} else if authored != nil {
		plan.Project = authored.Project
	} else {
		return plan, fmt.Errorf("Package %q does not provide a project-plan hook; supply an existing .hostlier project", name)
	}
	p := &plan.Project
	// Local artifact substitution requires a compatible reviewed source.
	if err := checkHookSource(hooks, p, name, arch); err != nil {
		return plan, err
	}
	// Apply explicit deployment routing before the packager sees this proposal.
	if err := s.resolvePackageRoute(p, hooks.OriginEnv()); err != nil {
		return plan, err
	}
	// Preserve the builder's input before applying contributions for the controller.
	if err := plan.compose(); err != nil {
		return plan, err
	}
	return plan, plan.validate(hooks)
}

// compose preserves an independent builder proposal while composing the controller workload.
func (plan *localProjectPlan) compose() error {
	// Give the builder its own reviewed project; auxiliary declarations only add to the deployment.
	data, err := json.Marshal(plan.Project)
	// Do not retain an incomplete artifact proposal.
	if err != nil {
		return err
	}
	plan.buildProject = data
	// Separate artifact inputs from the combined infrastructure plan.
	if len(plan.Contributions) > 0 {
		var combined model.Project
		// Clone nested maps so composition cannot mutate the original proposal.
		if err := json.Unmarshal(data, &combined); err != nil {
			return err
		}
		// A collision invalidates the composed plan before any remote work.
		if err := mergeContributions(&combined, plan.Contributions); err != nil {
			return err
		}
		plan.Project = combined
	}
	return nil
}

// validate checks the combined workload with the existing deferred-consent rules.
func (plan *localProjectPlan) validate(hooks *packageexec.ProjectHooks) error {
	check := plan.Project
	// Only an unresolved identity participates in the consent exchange.
	if minionInitPending(check) {
		// Consent requires a compiler artifact that can be checked against the plan.
		if !hooks.Has("project-package") {
			return fmt.Errorf("A pending Fused identity requires a packaged consent manifest")
		}
		// Reject a missing or malformed consent scope before any build or grant request.
		if _, err := reviewedFusedScope(plan.Declarations, check.Name); err != nil {
			return err
		}
		// Validate conflicts and the connection before packaging. Consent supplies the actual runtime UUID.
		pending := *check.Fused
		pending.AppID = "00000000-0000-4000-8000-000000000001"
		check.Fused = &pending
	}
	return config.ValidateInput(check)
}

// checkHookSource ensures the artifact can only replace the declared primary container source.
func checkHookSource(hooks *packageexec.ProjectHooks, p *model.Project, name, arch string) error {
	// Require the modern typed source and endpoint contract.
	if p.Version != 2 || p.Source == nil {
		return fmt.Errorf("Package %q requires a Hostlier v2 project with an explicit source", name)
	}
	// Source replacement applies only to a package providing the packaging stage.
	if hooks.Has("project-package") {
		// Registry images and prebuilt archives must not be silently replaced.
		if p.Source.Container == nil || p.Source.Container.Context == "" {
			return fmt.Errorf("Package %q returns a container context and needs a matching source.container declaration", name)
		}
		// Both halves of the protocol must agree on the eventual artifact and CPU.
		if hooks.Has("project-plan") && (p.Source.Container.Context != "." || p.Source.Container.Architecture != arch) {
			return fmt.Errorf("Package %q must plan container context . for target %s", name, arch)
		}
		p.Source.Container.Context = "."
		p.Source.Container.Architecture = arch
	}
	return nil
}

// resolvePackageRoute keeps the authored primary listener and its environment origin in sync.
func (s *session) resolvePackageRoute(p *model.Project, originEnv string) error {
	endpoint := p.Endpoint
	primary := model.PrimaryEndpointName(*p)
	// Named primary endpoints take precedence over their compatibility projection.
	if value, ok := p.Endpoints[primary]; ok {
		endpoint = &value
	}
	// Leave private primary listeners untouched.
	if endpoint == nil || endpoint.Exposure != "public" {
		// A route override cannot change the reviewed network exposure.
		if s.flags["route"] != "" {
			return fmt.Errorf("--route requires a public primary endpoint")
		}
		return nil
	}
	route, err := s.packageEndpointRoute(endpoint.Route)
	// Cancellation leaves the proposal untouched.
	if err != nil {
		return err
	}
	// Standalone local plans may leave a public hostname undecided.
	if route == "" {
		return nil
	}
	parsed, err := config.ParseRoute(route, nil, "")
	// Validate the authority and path before writing them into the project.
	if err != nil {
		return err
	}
	endpoint.Route = strings.TrimPrefix(parsed.URL, "https://")
	setPackageEndpoint(p, primary, *endpoint, originEnv, parsed.Hostname)
	return nil
}

// setPackageEndpoint updates both endpoint representations and the optional origin binding.
func setPackageEndpoint(p *model.Project, primary string, endpoint model.Endpoint, originEnv, hostname string) {
	// Keep named listeners consistent with the primary view.
	if len(p.Endpoints) > 0 {
		p.Endpoints[primary] = endpoint
	}
	// The v2 singular endpoint also carries the resulting route.
	if p.Endpoint != nil {
		p.Endpoint.Route = endpoint.Route
	}
	// Packages opt in to an origin binding independently of their identities.
	if originEnv != "" {
		// A generated origin may be the only environment value in the app.
		if p.Env == nil {
			p.Env = map[string]string{}
		}
		p.Env[originEnv] = "https://" + hostname
	}
}

// packageEndpointRoute delays choosing an absent public hostname until deployment.
func (s *session) packageEndpointRoute(authored string) (string, error) {
	route := defaultValue(s.flags["route"], authored)
	// Preserve a selected route and avoid prompting during local preview.
	if route != "" || s.command == "plan" {
		return route, nil
	}
	// Headless deploys must provide the public address explicitly.
	if !s.interactive {
		return "", fmt.Errorf("Supply the deployment hostname with --route HOST")
	}
	return s.ask("Full public address: ", false)
}

// deployPackage keeps isolated output alive until the ordinary artifact builder has consumed it.
func (s *session) deployPackage(hooks *packageexec.ProjectHooks, plan localProjectPlan, authored *config.ProjectInput) error {
	// Resolve the locally authored credentials before starting a potentially lengthy build.
	if err := s.preparePackageSecretSources(plan.Project, authored); err != nil {
		return err
	}
	p := plan.Project
	// Packages with only a plan hook keep the ordinary source pipeline.
	if hooks.Has("project-package") {
		out, err := os.MkdirTemp("", "hostlier-project-package-")
		// Use isolated output even for concurrent deploys of the same project.
		if err != nil {
			return err
		}
		defer os.RemoveAll(out)
		// Send only the actual proposal, before substituting the isolated artifact path or binding credentials.
		project := plan.buildProject
		result, err := hooks.Package(s.ctx, out, plan.Architecture, project, plan.Declarations)
		// Never upload a partial or rejected package output.
		if err != nil {
			return err
		}
		// Changes to dependencies or scope invalidate the prior proposal.
		if err := s.confirmPackageDeclarations(hooks, plan); err != nil {
			return err
		}
		context := filepath.Join(out, result.Artifact.Path)
		p.Source.Container.Context = context
		// Bind an unresolved identity through the ordinary OAuth workflow.
		if minionInitPending(p) {
			// A failed consent leaves the temporary output eligible for cleanup.
			if err = s.bindHookFused(&p, plan.Declarations, context); err != nil {
				return err
			}
		}
		// For an existing image, check contributions here because no packager performed that check.
	} else if err := s.collect(plan.enrolled, plan.Architecture, true); err != nil {
		return err
	}
	p, err := s.prepareDeploymentProject(p)
	// Only a verified immutable image is submitted for controller review.
	if err != nil {
		return err
	}
	return s.reviewDeployment(p, "")
}

// Retain explicit local secret sources only when their references remain in the proposed deployment.
func (s *session) preparePackageSecretSources(p model.Project, authored *config.ProjectInput) error {
	// Reference-only inputs need no new secret uploads.
	if authored == nil || len(authored.Secrets) == 0 {
		return nil
	}
	refs := map[string]bool{}
	collectPackageSecretRefs(p, refs)
	// Only previously authored sources can supply new stored values.
	for _, source := range authored.Secrets {
		// A removed reference cannot authorize an unrelated secret upload.
		if !refs[source.Name] {
			continue
		}
		value, err := s.projectSecretValue(source)
		// Detect a missing environment source before packaging anything.
		if err != nil {
			return err
		}
		s.projectSecrets = append(s.projectSecrets, projectSecret{name: source.Name, value: value})
	}
	return nil
}

// collectPackageSecretRefs includes references retained by sidecar workloads.
func collectPackageSecretRefs(p model.Project, refs map[string]bool) {
	// The reference is the stored name, independent of its local environment alias.
	for _, name := range p.Secrets {
		refs[name] = true
	}
	// Nested members carry their own independent references.
	for _, member := range p.Sidecars {
		collectPackageSecretRefs(member, refs)
	}
}

// Check package-owned declarations a second time before uploading or opening consent.
func (s *session) confirmPackageDeclarations(hooks *packageexec.ProjectHooks, plan localProjectPlan) error {
	// Contributors must still match before OAuth, artifact upload, or controller mutation.
	if err := s.collect(plan.enrolled, plan.Architecture, true); err != nil {
		return err
	}
	// Package-only selections already have an immutable snapshot of the authored project.
	if plan.original == nil {
		return nil
	}
	current, err := hooks.Plan(s.ctx, plan.Architecture)
	// A second planner failure invalidates the artifact.
	if err != nil {
		return err
	}
	// Compare both the deployment shape and auxiliary permission declarations.
	if !sameHookJSON(current.Project, plan.original.Project) || !sameHookJSON(current.Declarations, plan.original.Declarations) {
		return fmt.Errorf("Package project or declarations changed during packaging; review a new plan")
	}
	return nil
}

// Compare structured JSON independently of insignificant object-key ordering.
func sameHookJSON(a, b []byte) bool {
	// Omitted optional declarations compare equal only to another omission.
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var first, second any
	da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	db.UseNumber()
	return da.Decode(&first) == nil && db.Decode(&second) == nil && reflect.DeepEqual(first, second)
}
