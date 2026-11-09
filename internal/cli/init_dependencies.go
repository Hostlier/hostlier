package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// initChoices tracks an explicit dependency policy without defaulting to deploying every workspace member.
type initChoices struct {
	mode  string
	named map[string]string
	used  map[string]bool
}

// parseInitChoices accepts a bulk policy or one independently selected mode per dependency.
func parseInitChoices(value string) (initChoices, error) {
	choices := initChoices{named: map[string]string{}, used: map[string]bool{}}
	// Bulk policies are deliberate noninteractive alternatives to the per-dependency prompt.
	if value == "" || value == "none" || value == "apps" || value == "sidecars" {
		choices.mode = value
		return choices, nil
	}
	for _, item := range strings.Split(value, ",") { // Explicit per-app assignments support mixed topologies.
		// Reject ambiguous assignments before any dependency is selected.
		if err := choices.add(item); err != nil {
			return choices, err
		}
	}
	return choices, nil
}

// add records one unique literal placement for an explicitly named dependency.
func (choices *initChoices) add(item string) error {
	parts := strings.SplitN(item, "=", 2)
	// Invalid policies cannot silently skip or expose an app.
	if len(parts) != 2 || parts[0] == "" {
		return fmt.Errorf("Use --dependencies none, apps, sidecars, or NAME=app|sidecar|skip")
	}
	mode := parts[1]
	// Accept only literal declared placements.
	if mode != "app" && mode != "sidecar" && mode != "skip" {
		return fmt.Errorf("Dependency mode must be app, sidecar or skip")
	}
	// Duplicate selections are ambiguous intent.
	if _, exists := choices.named[parts[0]]; exists {
		return fmt.Errorf("Dependency choice repeated")
	}
	choices.named[parts[0]] = mode
	return nil
}

// initDependencyOwners resolves the selected runtime graph before a single config file is written.
func (s *session) initDependencyOwners(apps map[string]initApp, root string) (map[string]string, error) {
	choices, err := parseInitChoices(s.flags["dependencies"])
	// Invalid policies must not reach interactive prompts.
	if err != nil {
		return nil, err
	}
	plan := initDependencyPlan{session: s, apps: apps, choices: choices, owners: map[string]string{root: root}, active: map[string]bool{}, done: map[string]bool{}}
	// Cycles and shared sidecars cannot be flattened silently.
	if err = plan.walk(root, root); err != nil {
		return nil, err
	}
	for name := range choices.named { // Misspelled or unrelated selections must remain visible to automation.
		if !plan.choices.used[name] {
			return nil, fmt.Errorf("Dependency selection %q is not in the included runtime graph", name)
		}
	}
	return plan.owners, nil
}

// initDependencyPlan assigns each included app to one lifecycle owner while traversing only selected dependencies.
type initDependencyPlan struct {
	session      *session
	apps         map[string]initApp
	choices      initChoices
	owners       map[string]string
	active, done map[string]bool
}

// walk follows dependencies only after their owner explicitly includes them for deployment.
func (plan *initDependencyPlan) walk(key, owner string) error {
	// Cycles have no unambiguous deployment order.
	if plan.active[key] {
		return fmt.Errorf("Runtime dependency cycle includes %q; resolve it before initialization", key)
	}
	// Shared separate apps are initialized once.
	if plan.done[key] {
		return nil
	}
	plan.active[key] = true
	dependencies := append([]string(nil), plan.apps[key].Dependencies...)
	sort.Strings(dependencies)
	for _, dependency := range dependencies { // Ask about actual runtime edges, not unrelated monorepo packages.
		// A failed placement leaves the whole scaffold unwritten.
		if err := plan.include(dependency, owner); err != nil {
			return err
		}
	}
	delete(plan.active, key)
	plan.done[key] = true
	return nil
}

// include prevents a shared dependency from being silently duplicated into unrelated sidecar lifecycles.
func (plan *initDependencyPlan) include(key, owner string) error {
	// Libraries must be filtered during discovery.
	if _, ok := plan.apps[key]; !ok {
		return fmt.Errorf("Runtime dependency %q has no deployable app definition", key)
	}
	mode, err := plan.choose(key)
	// Declined dependencies keep their existing deployment ownership.
	if err != nil || mode == "skip" {
		return err
	}
	// Separate apps keep independent lifecycle and storage.
	if mode == "app" {
		owner = key
	}
	// A sidecar cannot belong to two independently deployed apps.
	if previous, exists := plan.owners[key]; exists && previous != owner {
		return fmt.Errorf("Dependency %q is shared by multiple apps; select it as a separate app", key)
	}
	plan.owners[key] = owner
	return plan.walk(key, owner)
}

// choose records each dependency decision once, including explicit skips shared by multiple parents.
func (plan *initDependencyPlan) choose(key string) (string, error) {
	// Reuse a prior prompt answer or authored per-app choice.
	if mode, ok := plan.choices.named[key]; ok {
		plan.choices.used[key] = true
		return mode, nil
	}
	mode := map[string]string{"none": "skip", "apps": "app", "sidecars": "sidecar"}[plan.choices.mode]
	if mode == "" { // No implicit placement is allowed for an unattended init.
		if !plan.session.interactive {
			return "", fmt.Errorf("App depends on %s; use --dependencies none, apps, sidecars, or NAME=MODE", key)
		}
		answer, err := plan.session.ask(fmt.Sprintf("Include runtime dependency %s for deployment? [skip/app/sidecar] (skip): ", key), false)
		// Interrupted input is not an approval to include dependencies.
		if err != nil {
			return "", err
		}
		mode = defaultValue(strings.ToLower(strings.TrimSpace(answer)), "skip")
	}
	// Misspelled choices require correction.
	if mode != "app" && mode != "sidecar" && mode != "skip" {
		return "", fmt.Errorf("Choose skip, app or sidecar for dependency %s", key)
	}
	plan.choices.named[key] = mode
	plan.choices.used[key] = true
	return mode, nil
}

// assembleInit produces separate config files only for independent apps and embeds explicitly chosen sidecars.
func assembleInit(apps map[string]initApp, root string, owners map[string]string) (map[string]string, []string, error) {
	projects, err := initProjects(apps, root, owners)
	// Readiness and topology issues must be resolved before writes.
	if err != nil {
		return nil, nil, err
	}
	files := map[string]string{}
	notes := []string{"Review detected settings, then run hostlier validate and hostlier deploy with the selected config. Init performs no deployment.", "The target architecture defaults to amd64; choose --architecture arm64 for an ARM server."}
	for _, key := range sortedInitKeys(owners) { // Only included runtime dependencies contribute scaffold files.
		app := apps[key]
		notes = append(notes, app.Notes...)
		// Competing generated recipes indicate ambiguous app ownership.
		if err := mergeInitScaffolds(files, app.Files); err != nil {
			return nil, nil, err
		}
	}
	for _, key := range sortedInitKeys(projects) { // Validate each complete main-app group independently.
		project := projects[key]
		check := project
		// Keep unresolved primary identity in the saved document, but validate the rest of the topology now.
		if minionInitPending(project) {
			check.Fused = nil
			notes = append(notes, "Fused identity will be bound by hostlier deploy. The saved file cannot deploy until then.")
		}
		// Unbound sidecars must be provisioned independently before assembling this group.
		if err := config.ValidateInput(check); err != nil {
			return nil, nil, fmt.Errorf("Invalid detected app %s: %w", key, err)
		}
		path := initOutputPath(apps, root, key)
		text, err := initProjectYAML(project, filepath.Dir(path))
		// Serialization failures leave the filesystem unchanged.
		if err != nil {
			return nil, nil, err
		}
		files[path] = text
	}
	// Routing changes are never concealed as Compose compatibility.
	if len(owners) > 1 {
		notes = append(notes, "Sidecars share localhost and require distinct ports. Separate apps have independent Hostlier URLs. Review inter-service addresses; Compose DNS and host-port mappings are not retained.")
	}
	return files, notes, nil
}

// initProjects copies models before applying lifecycle choices so detected candidates remain reusable.
func initProjects(apps map[string]initApp, root string, owners map[string]string) (map[string]model.Project, error) {
	result := map[string]model.Project{}
	for key, owner := range owners { // Every independent app gets its own deployment document.
		// Sidecars are attached after all main-app containers exist.
		if key != owner {
			continue
		}
		project, err := copyInitProject(apps[key].Project)
		// Typed public metadata must remain serializable.
		if err != nil {
			return nil, err
		}
		if key != root { // Dependencies stay private unless the user explicitly edits their generated config.
			if err := makeInitDependency(&project, apps[key], "private"); err != nil {
				return nil, err
			}
			project.Endpoint.Consumers = initConsumers(apps, owners, key)
		}
		result[key] = project
	}
	// Sidecars attach only after every independently deployed owner exists.
	if err := attachInitSidecars(result, apps, owners); err != nil {
		return nil, err
	}
	return result, nil
}

// attachInitSidecars binds included members to their reviewed lifecycle owner without mutating discovery metadata.
func attachInitSidecars(result map[string]model.Project, apps map[string]initApp, owners map[string]string) error {
	for _, key := range sortedInitKeys(owners) { // Attach each included sidecar to its selected lifecycle owner.
		owner := owners[key]
		// Independently deployed apps are never duplicated as sidecars.
		if key == owner {
			continue
		}
		project, err := copyInitProject(apps[key].Project)
		// Avoid sharing mutable model pointers between app groups.
		if err != nil {
			return err
		}
		// Private sidecars require executable readiness probes.
		if err := makeInitDependency(&project, apps[key], "loopback"); err != nil {
			return err
		}
		parent := result[owner]
		// Allocate the member map only when explicitly needed.
		if parent.Sidecars == nil {
			parent.Sidecars = map[string]model.Project{}
		}
		project.Name = ""
		project.Version = 0
		parent.Sidecars[apps[key].Name] = project
		result[owner] = parent
	}
	return nil
}

// copyInitProject separates mutable endpoint and environment maps without changing their declaration semantics.
func copyInitProject(project model.Project) (model.Project, error) {
	data, err := json.Marshal(project)
	// A partial clone cannot participate in topology validation.
	if err != nil {
		return model.Project{}, err
	}
	var result model.Project
	err = json.Unmarshal(data, &result)
	return result, err
}

// makeInitDependency requires a real internal readiness check rather than inventing a successful probe.
func makeInitDependency(project *model.Project, app initApp, exposure string) error {
	project.Endpoint.Route = ""
	// Listenerless workers retain exposure none.
	if project.Workload != "worker" {
		project.Endpoint.Exposure = exposure
	}
	// Compose's explicit probe remains authoritative.
	if project.Health != nil && len(project.Health.Exec) > 0 {
		return nil
	}
	// Unknown images may not contain curl or a shell.
	if app.Kind != "node" && app.Kind != "minion" {
		return fmt.Errorf("Dependency %s needs an executable healthcheck before using it as a private app or sidecar", app.Name)
	}
	path := "/"
	// Preserve a known Minion readiness path.
	if project.Health != nil {
		path = project.Health.Path
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", project.Endpoint.Port, path)
	script := fmt.Sprintf("fetch(%q).then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))", url)
	project.Health = &model.Health{Exec: []string{"node", "-e", script}, TimeoutSeconds: 30}
	return nil
}

// initConsumers authorizes only owning apps with an actual runtime edge to a separate dependency.
func initConsumers(apps map[string]initApp, owners map[string]string, dependency string) []string {
	consumers := map[string]bool{}
	for key, owner := range owners { // Sidecar dependencies belong to their main app's network identity.
		for _, target := range apps[key].Dependencies {
			// Never manufacture self-access grants.
			if target == dependency && owner != dependency {
				consumers[apps[owner].Name] = true
			}
		}
	}
	return sortedInitKeys(consumers)
}

// initOutputPath keeps monorepo configs beside each app and distinguishes Compose services sharing one directory.
func initOutputPath(apps map[string]initApp, root, key string) string {
	directory := apps[key].Directory
	// Compose main apps cannot overwrite the selected app's config.
	if key != root && directory == apps[root].Directory {
		return filepath.Join(directory, ".hostlier."+apps[key].Name)
	}
	return filepath.Join(directory, ".hostlier")
}

// mergeInitScaffolds rejects conflicting recipes instead of allowing map order to choose a build.
func mergeInitScaffolds(files map[string]string, extra map[string][]byte) error {
	for path, data := range extra { // Identical shared ignore files can safely be emitted once.
		// Different apps must own distinct build definitions.
		if existing, ok := files[path]; ok && existing != string(data) {
			return fmt.Errorf("Conflicting generated build file: %s", path)
		}
		files[path] = string(data)
	}
	return nil
}
