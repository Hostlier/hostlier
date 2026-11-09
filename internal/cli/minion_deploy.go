package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// minionDeployment owns one fresh build for the duration of an ordinary deployment.
type minionDeployment struct {
	artifact  string
	declared  string
	requested *model.FusedConnection
	project   model.Project
}

// prepareMinionDeployment activates only for an implicit deploy in a Minion source directory.
func (s *session) prepareMinionDeployment() (*minionDeployment, func(), error) {
	// Explicit artifacts, plans and rollbacks keep their existing source contracts.
	if s.command != "deploy" || s.flags["file"] != "" {
		return nil, minionNoCleanup, nil
	}
	path := filepath.Join(s.cwd, "minion.json")
	// Missing metadata is an ordinary non-Minion application.
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, minionNoCleanup, nil
	}
	root, err := filepath.EvalSymlinks(s.cwd)
	// Compare resolved build contexts against the same physical app root (including macOS /var).
	if err != nil {
		return nil, minionNoCleanup, err
	}
	s.cwd = root
	var project *model.Project
	if _, statErr := os.Lstat(s.projectPath()); !os.IsNotExist(statErr) {
		input, err := config.ReadProjectSources(s.projectPath(), "")
		if err != nil {
			return nil, minionNoCleanup, err
		}
		if input.Project.Package != "" && input.Project.Package != "minion" {
			return nil, minionNoCleanup, nil
		}
		project = &input.Project
	}
	setup, err := readMinionInitSetup(root)
	// Reject malformed or symlinked metadata before installing any package.
	if err != nil {
		return nil, minionNoCleanup, err
	}
	declared, err := minionInitContext(s.cwd, defaultValue(setup.value("outdir"), "build"))
	// Only an app-owned source path may be replaced by this invocation's fresh artifact.
	if err != nil {
		return nil, minionNoCleanup, err
	}
	dir, err := os.MkdirTemp("", "hostlier-minion-deploy-")
	// No authored output directory is shared across concurrent deployments.
	if err != nil {
		return nil, minionNoCleanup, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) } // Remove only this invocation's private staging directory.
	d := &minionDeployment{requested: setup.Fused, declared: declared, artifact: filepath.Join(dir, "release", "artifact")}
	// Reject competing authored identities before installation or a remote consent request.
	if err = s.checkMinionProject(setup, d, project); err == nil {
		err = s.compileMinionDeployment(setup, d)
	}
	return d, cleanup, err
}

// checkMinionProject checks the authored source identity before changing project or controller state.
func (s *session) checkMinionProject(setup *minionInitSetup, d *minionDeployment, project *model.Project) error {
	if project == nil {
		return nil
	}
	endpoint := project.Endpoint
	if primary, ok := project.Endpoints[model.PrimaryEndpointName(*project)]; ok {
		endpoint = &primary
	}
	if endpoint != nil && endpoint.Route != "" {
		setup.set("route", endpoint.Route)
	}
	if project.Source != nil && project.Source.Container != nil && project.Source.Container.Architecture != "" {
		setup.set("architecture", project.Source.Container.Architecture)
	}
	d.project.Name = setup.value("name")
	d.project.Fused = setup.Fused
	return d.apply(project, filepath.Dir(s.projectPath()))
}

// readMinionProject overlays a fresh local build before preparing the deployment inputs.
func (s *session) readMinionProject(d *minionDeployment) (model.Project, error) {
	p, err := s.readProjectInput("")
	// Other project types and explicit deployment files keep their authored sources.
	if d != nil && err == nil {
		err = d.apply(&p, filepath.Dir(s.projectPath()))
		if err == nil && d.project.Env["MINION_ORIGIN"] != "" {
			if p.Env == nil {
				p.Env = map[string]string{}
			}
			p.Env["MINION_ORIGIN"] = d.project.Env["MINION_ORIGIN"]
		}
	}
	return p, err
}

// configureMinionDeploy resolves deployment identity and the public route before starting a build.
func (s *session) configureMinionDeploy(setup *minionInitSetup) error {
	name := setup.value("name")
	if !minionName.MatchString(name) {
		return fmt.Errorf("Set a valid app name in minion.json or run hostlier init")
	}
	if route := s.flags["route"]; route != "" {
		setup.set("route", route)
	}
	if setup.value("route") == "" && !s.interactive {
		return fmt.Errorf("Minion needs a deployment hostname: run hostlier deploy --route HOST or hostlier init")
	}
	return s.minionInitField(setup, "route", "Deployment hostname", "")
}

// compileMinionDeployment invokes the installed compiler with a transient sibling config so all entry paths retain their meaning.
func (s *session) compileMinionDeployment(setup *minionInitSetup, d *minionDeployment) error {
	if err := s.configureMinionDeploy(setup); err != nil {
		return err
	}
	// Use the same version gate and release package lookup as the standalone Minion commands.
	if err := s.requireMinionNode(); err != nil {
		return err
	}
	child := *s
	child.out = s.errOut
	script, err := child.ensureMinionCLI(s.cwd)
	// Installation failures stop before compilation or remote access.
	if err != nil {
		return err
	}
	arch := defaultValue(setup.value("architecture"), "amd64")
	outdir := filepath.Dir(d.artifact)
	protocol, err := minionBuildProtocol(script)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.errOut, "Building Minion application")
	if protocol == 0 {
		err = child.buildLegacyMinion(script, setup, outdir, arch)
	} else {
		args := []string{script, "build", filepath.Join(s.cwd, "minion.json"), "--outdir", outdir, "--architecture", arch, "--route", setup.value("route")}
		err = child.runMinionProcess(s.cwd, "node", args, s.in, s.errOut, s.errOut)
	}
	if err != nil {
		return err
	}
	return child.bindMinionDeployment(d, setup)
}

// bindMinionDeployment uses only the fresh compiler output and preserves the parent's deployment target and flags.
func (s *session) bindMinionDeployment(d *minionDeployment, setup *minionInitSetup) error {
	pending := filepath.Join(d.artifact, "hostlier-pending.json")
	// Compiler-declared native integration requires an actual grant before planning.
	if setup.Fused != nil && setup.Fused.AppID == "" {
		p, manifest, err := readPendingMinion(pending)
		// Verify the reviewed app name and manifest before choosing controller authority.
		if err != nil {
			return err
		}
		// Reject a different app before opening OAuth.
		if p.Name != setup.value("name") {
			return fmt.Errorf("Compiled Minion identity differs from minion.json")
		}
		// A failed or declined grant cannot reach packaging.
		if err = s.provisionMinionDeployment(pending, p, manifest); err != nil {
			return err
		}
	}
	path := filepath.Join(d.artifact, ".hostlier")
	input, err := config.ReadProjectSources(path, "")
	// Compiler artifacts may reference stored secrets, but must never upload a local literal.
	if err != nil {
		return err
	}
	// The compiler must emit only stored secret references for this app.
	if len(input.Secrets) != 0 || input.Project.Name != setup.value("name") {
		return fmt.Errorf("Invalid compiled Minion deployment identity or secret references")
	}
	d.project = input.Project
	// A root deployment document remains authoritative for routes, sidecars and resources.
	if _, err = os.Lstat(s.projectPath()); os.IsNotExist(err) {
		s.flags["file"] = path
	}
	return nil
}

// provisionMinionDeployment keeps grants for different requested permissions independent and versions within one scope together.
func (s *session) provisionMinionDeployment(path string, p pendingMinionProject, manifest string) error {
	var status struct {
		Connections map[string]runtimeConnectionStatus `json:"connections"`
	}
	// Reuse an exact existing manual provisioning when upgrading to automatic deploy.
	if err := s.remote.Request(s.ctx, "GET", "/v1/fused", nil, &status); err != nil {
		return err
	}
	s.target = p.Name
	// Existing installs can keep their manually provisioned binding.
	if status.Connections[p.Name].matches(p) {
		return s.bindMinionProject(path, p)
	}
	var scope map[string]json.RawMessage
	// Canonical permissions exclude the release version, which the existing grant can publish.
	if err := json.Unmarshal([]byte(manifest), &scope); err != nil {
		return err
	}
	delete(scope, "version")
	scopeData, err := json.Marshal([]any{p.Name, p.Consent.EngineURL, p.Fused.GRPCURL, p.Fused.TokenSecret, scope})
	// A stable grant name lets a cancelled local command resume through the normal controller protocol.
	if err != nil {
		return err
	}
	sum := sha256.Sum256(scopeData)
	s.target = p.Name + "-" + hex.EncodeToString(sum[:8])
	// Each manifest gets a distinct credential reference so an older revision remains deployable.
	secretHash := sha256.Sum256([]byte(s.target + p.Consent.Digest))
	base := p.Fused.TokenSecret
	// Leave room for the digest suffix within the controller secret-name limit.
	if len(base) > 100 {
		base = base[:100]
	}
	p.Fused.TokenSecret = base + "-" + hex.EncodeToString(secretHash[:8])
	return s.provisionPendingMinion(path, p, manifest, true)
}

// apply selects the current build while preserving the user's deployment document and its relative paths.
func (d *minionDeployment) apply(p *model.Project, root string) error {
	// Explicit source overrides must not silently package some other app on a plain deploy.
	if p.Name != d.project.Name || p.Source == nil || p.Source.Container == nil {
		return fmt.Errorf("Minion deployment requires its local container source; use deploy --file for another project")
	}
	container := p.Source.Container
	context := container.Context
	// Source declarations are relative to their own project document.
	if !filepath.IsAbs(context) {
		context = filepath.Join(root, context)
	}
	// Only the authored output or the current fresh build may be substituted.
	if filepath.Clean(context) != filepath.Clean(d.declared) && filepath.Clean(context) != filepath.Clean(d.artifact) {
		return fmt.Errorf("Minion build context differs from the deployment file; use deploy --file for an explicit context")
	}
	container.Context = d.artifact
	// Conflicting runtime identities must be resolved explicitly rather than changing authority.
	if p.Fused != nil && !sameMinionIdentity(p.Fused, d.requested) && !sameMinionIdentity(p.Fused, d.project.Fused) {
		return fmt.Errorf("Minion Fused identity differs from the deployment file")
	}
	p.Fused = d.project.Fused
	return nil
}

// sameMinionIdentity compares a declared runtime binding to a known requested or compiled identity.
func sameMinionIdentity(binding, expected *model.FusedConnection) bool {
	return expected != nil && *binding == *expected
}

// fusedHookScope is the public consent request reviewed alongside the package's project.
type fusedHookScope struct {
	EngineURL string          `json:"engineUrl"`
	Manifest  json.RawMessage `json:"manifest"`
}

// reviewedFusedScope preserves the entire manifest while extracting the identity needed for local validation.
func reviewedFusedScope(declarations json.RawMessage, app string) (fusedHookScope, error) {
	var review fusedHookScope
	var manifest struct {
		Name string `json:"name"`
	}
	// Bind the bounded permission document to the application named in this plan.
	if json.Unmarshal(declarations, &review) != nil || json.Unmarshal(review.Manifest, &manifest) != nil || manifest.Name != app || len(review.Manifest) > 65536 {
		return review, fmt.Errorf("Expected a reviewed Engine and Fused manifest for this app")
	}
	origin, err := url.Parse(review.EngineURL)
	// The reviewed destination must be a suitable Engine origin before building or registering anything.
	if err != nil || !validHookEngineURL(origin) {
		return review, fmt.Errorf("Invalid Fused consent Engine origin")
	}
	return review, nil
}

// validHookEngineURL accepts only Engine HTTP origins. The ordinary connection validator enforces TLS except on loopback.
func validHookEngineURL(u *url.URL) bool {
	// Consent is an HTTP exchange even when the separate execution transport uses gRPC.
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	test := model.FusedConnection{GRPCURL: u.String(), AppID: "00000000-0000-4000-8000-000000000001", TokenSecret: "validation"}
	return config.ValidateFusedConnection(test) == nil
}

// bindHookFused binds exactly the reviewed scope while preserving the chosen workload topology.
func (s *session) bindHookFused(project *model.Project, declarations json.RawMessage, context string) error {
	review, err := reviewedFusedScope(declarations, project.Name)
	// A missing review cannot authorize an identity request.
	if err != nil {
		return err
	}
	pendingPath := filepath.Join(context, "hostlier-pending.json")
	pending, manifest, err := readPendingMinion(pendingPath)
	// An incomplete or changed compiler manifest stops before contacting the controller.
	if err != nil {
		return err
	}
	// A widened operation, bucket, or changed credential source needs a new plan.
	if !review.matches(project, pending, manifest) {
		return fmt.Errorf("Compiled Minion Fused access differs from the package plan; review a new plan")
	}
	// Only successful grant reuse or explicit consent can release a runtime credential.
	if err = s.provisionMinionDeployment(pendingPath, pending, manifest); err != nil {
		return err
	}
	return applyBoundHookIdentity(project, context)
}

// matches compares the exact identity, engine and operation/bucket scope.
func (review fusedHookScope) matches(project *model.Project, pending pendingMinionProject, manifest string) bool {
	return pending.Name == project.Name && sameMinionIdentity(pending.Fused, project.Fused) && review.EngineURL == pending.Consent.EngineURL && sameHookJSON(review.Manifest, []byte(manifest))
}

// applyBoundHookIdentity accepts only the runtime identity returned through the controller.
func applyBoundHookIdentity(project *model.Project, context string) error {
	input, err := config.ReadProjectSources(filepath.Join(context, ".hostlier"), "")
	// The verified controller response must be recoverable as a bound artifact.
	if err != nil {
		return err
	}
	// An export cannot substitute another app or introduce new local secret sources.
	if input.Project.Fused == nil || input.Project.Fused.AppID == "" || input.Project.Name != project.Name || len(input.Secrets) > 0 {
		return fmt.Errorf("Invalid bound Minion identity")
	}
	project.Fused = input.Project.Fused
	return nil
}
