package cli

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"hostlier/internal/model"
)

// composeNames keeps detected identities compatible with Hostlier storage and workload names.
var composeNames = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// composeEnvNames bounds identifiers before deriving collision-free stored secret references.
var composeEnvNames = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// composeImages admits literal registry references, never Compose interpolation or commands.
var composeImages = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,511}$`)

// detectComposeApps reads a bounded declarative subset without resolving environment files or executing builds.
func detectComposeApps(root, filename, architecture string) (map[string]initApp, error) {
	document, err := readComposeDocument(root, filename)
	// Syntax and unsupported document fields must fail before candidates can be offered.
	if err != nil {
		return nil, err
	}
	services, err := composeMapping(document["services"], "services", nil)
	// An empty document cannot supply an application selection.
	if err != nil || len(services) == 0 || len(services) > 64 {
		return nil, fmt.Errorf("Compose requires 1 to 64 services")
	}
	volumes, err := composeDeclaredVolumes(document["volumes"])
	// Storage driver semantics must be understood even when a volume is not yet selected.
	if err != nil {
		return nil, err
	}
	apps := map[string]initApp{}
	owners := map[string]string{}
	// Sorting makes duplicate ownership errors and candidate construction deterministic.
	for _, name := range composeSortedKeys(services) {
		app, detectErr := composeApplication(root, filename, name, architecture, services[name], volumes, owners)
		// A partially translated topology must never be presented as a complete Compose conversion.
		if detectErr != nil {
			return nil, fmt.Errorf("Compose service %s: %w", name, detectErr)
		}
		apps[name] = app
	}
	// Validate dependency edges after every candidate has been declared.
	if err := composeDependencyReferences(apps); err != nil {
		return nil, err
	}
	return apps, nil
}

// composeDependencyReferences rejects invented dependencies before caller selection.
func composeDependencyReferences(apps map[string]initApp) error {
	// References are checked after all services exist, independently of source ordering.
	for name, app := range apps {
		// Dependency selection is performed by the caller, but names must be real and non-self-referential.
		for _, dependency := range app.Dependencies {
			// Missing declarations cannot become invented resources or implicit public services.
			if _, ok := apps[dependency]; !ok || dependency == name {
				return fmt.Errorf("Compose service %s has an invalid depends_on reference", name)
			}
		}
	}
	return nil
}

// readComposeDocument bounds bytes and tree structure before validating recognized root fields.
func readComposeDocument(root, filename string) (map[string]*yaml.Node, error) {
	file, err := composeLocalPath(root, filename)
	// Resolve symlinks before opening so a selected document cannot escape its project directory.
	if err != nil {
		return nil, err
	}
	data, err := composeReadFile(file)
	// Regular bounded files are the only permitted YAML inputs.
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var tree, trailing yaml.Node
	// Parser diagnostics may contain source values, so only a fixed syntax error crosses this boundary.
	if decoder.Decode(&tree) != nil || len(tree.Content) != 1 {
		return nil, fmt.Errorf("Invalid Compose YAML syntax")
	}
	// Multiple documents have no unambiguous single Compose application meaning.
	if decoder.Decode(&trailing) != io.EOF {
		return nil, fmt.Errorf("Compose must contain one YAML document")
	}
	remaining := 10000
	// Aliases and deeply nested input cannot expand into unexpectedly large declarative graphs.
	if err := composeTree(&tree, 0, &remaining); err != nil {
		return nil, err
	}
	return composeMapping(tree.Content[0], "document", []string{"services", "volumes", "version"})
}

// composeReadFile rejects devices and unbounded documents before parsing their contents.
func composeReadFile(file string) ([]byte, error) {
	info, err := os.Stat(file)
	// FIFOs and device nodes must not block detection or produce unbounded streams.
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Compose input must be a regular file")
	}
	input, err := os.Open(file)
	// Report only the operation, never data that could include environment values.
	if err != nil {
		return nil, fmt.Errorf("Cannot read Compose file")
	}
	defer input.Close()
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	// A hard byte limit bounds parser work independently of service count.
	if err != nil || len(data) > 1<<20 {
		return nil, fmt.Errorf("Compose file must be at most 1 MiB")
	}
	return data, nil
}

// composeTree rejects aliases, custom tags and excessive recursion without decoding arbitrary Go values.
func composeTree(node *yaml.Node, depth int, remaining *int) error {
	*remaining--
	// Anchors and merges are deliberately unsupported rather than silently partially expanded.
	if depth > 32 || *remaining < 0 || node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("Compose aliases, anchors, or excessive nesting are unsupported; inline the configuration")
	}
	// Custom YAML tags do not belong to the supported Compose data model.
	if node.Tag != "" && !strings.HasPrefix(node.Tag, "!!") {
		return fmt.Errorf("Compose custom YAML tags are unsupported")
	}
	// Inspect descendants before any mapping helper can interpret their values.
	for _, child := range node.Content {
		// Stop at the first invalid subtree to preserve the same resource bounds everywhere.
		if err := composeTree(child, depth+1, remaining); err != nil {
			return err
		}
	}
	return nil
}

// composeMapping validates string-keyed maps, duplicate keys and an optional explicit field allowlist.
func composeMapping(node *yaml.Node, where string, fields []string) (map[string]*yaml.Node, error) {
	// Required mappings cannot be replaced with scalars, sequences, or null.
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("Compose %s must be a mapping", where)
	}
	result := map[string]*yaml.Node{}
	allowed := map[string]bool{}
	// A nil allowlist is reserved for user-owned service, volume, and environment names.
	for _, key := range fields {
		allowed[key] = true
	}
	// YAML maps contain alternating key and value nodes.
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		// Duplicate and non-string keys must never overwrite previously inspected data.
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || result[key.Value] != nil {
			return nil, fmt.Errorf("Compose %s has duplicate or invalid keys", where)
		}
		// Unsupported behavior requires a deliberate authored Hostlier translation.
		if fields != nil && !allowed[key.Value] {
			return nil, fmt.Errorf("Compose %s field %q is unsupported; configure it explicitly in Hostlier or remove it", where, key.Value)
		}
		result[key.Value] = node.Content[i+1]
	}
	return result, nil
}

// composeLocalPath confines actual source paths to the selected root, including symlink targets.
func composeLocalPath(root, value string) (string, error) {
	base, err := filepath.EvalSymlinks(root)
	// A project root must already exist before detection can inspect its inputs.
	if err != nil {
		return "", fmt.Errorf("Cannot resolve Compose project directory")
	}
	base, err = filepath.Abs(base)
	// Absolute roots make all later containment checks independent of process working directory.
	if err != nil {
		return "", fmt.Errorf("Cannot resolve Compose project directory")
	}
	candidate := value
	// Relative Compose paths are rooted beside the selected project document.
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(base, value)
	}
	actual, err := filepath.EvalSymlinks(candidate)
	// Remote, nonexistent and symlink-escaping contexts are not portable local build inputs.
	if err != nil {
		return "", fmt.Errorf("Compose path must exist inside the project directory")
	}
	relative, err := filepath.Rel(base, actual)
	// A sibling with a matching string prefix still lies outside the root.
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Compose path escapes the project directory")
	}
	return actual, nil
}

// composeApplication translates one service while retaining dependency selection and migration notes.
func composeApplication(root, filename, name, architecture string, node *yaml.Node, volumes map[string]bool, owners map[string]string) (initApp, error) {
	app := initApp{Name: name, Directory: root, Kind: "compose", Files: map[string][]byte{}}
	// Service identities remain stable across selectors, secret references and sidecar names.
	if !composeNames.MatchString(name) {
		return app, fmt.Errorf("Rename the service to lowercase letters, digits and hyphens (maximum 63 characters)")
	}
	fields, err := composeMapping(node, "service", []string{"image", "build", "ports", "expose", "environment", "depends_on", "volumes", "command", "entrypoint", "healthcheck"})
	// Unsupported fields are never dropped merely because another field can be translated.
	if err != nil {
		return app, err
	}
	source, err := composeSource(root, filename, architecture, fields)
	// Build contexts and image references have separate portability constraints.
	if err != nil {
		return app, err
	}
	port, err := composeServicePort(fields, source.Image)
	// Every supported candidate declares one concrete TCP listener.
	if err != nil {
		return app, err
	}
	app.Project = model.Project{Version: 2, Name: name, Source: source, Endpoint: &model.Endpoint{Exposure: "public", Port: port}}
	// Process, environment, readiness and storage share the same strict translation boundary.
	if err := composeServiceOptions(&app, fields, volumes, owners); err != nil {
		return app, err
	}
	app.Notes = append(app.Notes, "Compose service DNS names are not preserved. For sidecars, update dependency URLs to localhost and the dependency port; separate apps require Hostlier private endpoint bindings.")
	// Host publications are deliberately reviewed again rather than copied into a public route.
	if fields["ports"] != nil {
		app.Notes = append(app.Notes, "Compose host port bindings are not preserved; Hostlier endpoint exposure and routing must be selected explicitly.")
	}
	return app, nil
}

// composeSource admits one literal image or an explicitly confined local container recipe.
func composeSource(root, filename, architecture string, fields map[string]*yaml.Node) (*model.Source, error) {
	image, build := fields["image"], fields["build"]
	// Pull-policy and image-tagging semantics make a combined image/build declaration ambiguous.
	if (image == nil) == (build == nil) {
		return nil, fmt.Errorf("Choose exactly one image or build; image plus build requires an explicit Hostlier source")
	}
	// Literal images require neither filesystem inspection nor local Docker execution.
	if image != nil {
		// Variables and malformed references must not be resolved from the shell or .env.
		if image.Kind != yaml.ScalarNode || !composeImages.MatchString(image.Value) {
			return nil, fmt.Errorf("Use a literal image reference without Compose interpolation")
		}
		return &model.Source{Image: image.Value}, nil
	}
	return composeContainerSource(root, filename, architecture, build)
}

// composeContainerSource resolves document-relative contexts and recipes without escaping their ownership boundary.
func composeContainerSource(root, filename, architecture string, node *yaml.Node) (*model.Source, error) {
	context, recipe, err := composeBuildSettings(node)
	// Every build option must be understood before looking up its input files.
	if err != nil {
		return nil, err
	}
	document, err := composeLocalPath(root, filename)
	// The selected document determines relative path meaning.
	if err != nil {
		return nil, err
	}
	resolved, err := composeLocalPath(root, filepath.Join(filepath.Dir(document), context))
	// Symlink and parent traversals cannot acquire source beyond the project root.
	if err != nil {
		return nil, err
	}
	recipe, err = composeRecipe(resolved, recipe)
	// Recipes remain inside their own build context, not merely somewhere in the project.
	if err != nil {
		return nil, err
	}
	// Cross-platform targets are explicit rather than inferred from the developer machine.
	if architecture != "amd64" && architecture != "arm64" {
		return nil, fmt.Errorf("Compose builds require amd64 or arm64 architecture")
	}
	return &model.Source{Container: &model.ContainerSource{Context: resolved, Dockerfile: recipe, Architecture: architecture}}, nil
}

// composeRecipe confines a Dockerfile to its context and normalizes the runtime's relative recipe name.
func composeRecipe(context, recipe string) (string, error) {
	resolved, err := composeLocalPath(context, recipe)
	// Missing or escaped Dockerfiles cannot become future accidental build inputs.
	if err != nil {
		return "", fmt.Errorf("Compose Dockerfile must exist inside its build context")
	}
	info, err := os.Stat(resolved)
	// Devices and directories cannot serve as authored container recipes.
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("Compose Dockerfile must be a regular file")
	}
	relative, err := filepath.Rel(context, resolved)
	// The source model stores a recipe relative to its already absolute context.
	if err != nil {
		return "", fmt.Errorf("Cannot resolve Compose Dockerfile")
	}
	return filepath.ToSlash(relative), nil
}

// composeBuildSettings recognizes only context and Dockerfile fields with literal local path semantics.
func composeBuildSettings(node *yaml.Node) (string, string, error) {
	// Scalar shorthand selects a context while retaining the standard Dockerfile name.
	if node.Kind == yaml.ScalarNode {
		context, err := composeLiteralPath(node, ".")
		return context, "Dockerfile", err
	}
	fields, err := composeMapping(node, "build", []string{"context", "dockerfile"})
	// Build args, secrets, targets and additional contexts require explicit user translation.
	if err != nil {
		return "", "", err
	}
	context, err := composeLiteralPath(fields["context"], ".")
	// A missing context defaults beside the document; an invalid authored one cannot be ignored.
	if err != nil {
		return "", "", err
	}
	recipe, err := composeLiteralPath(fields["dockerfile"], "Dockerfile")
	return context, recipe, err
}

// composeLiteralPath accepts relative local syntax without interpreting shell or Compose variables.
func composeLiteralPath(node *yaml.Node, fallback string) (string, error) {
	// Omitted build fields use Compose's ordinary local defaults.
	if node == nil {
		return fallback, nil
	}
	value := node.Value
	// Remote references, absolute paths and invalid scalars are outside this local detector's authority.
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || value == "" || strings.ContainsAny(value, "$\x00") || strings.Contains(value, "://") || filepath.IsAbs(value) {
		return "", fmt.Errorf("Compose build paths must be literal relative local paths")
	}
	return value, nil
}

// composeServiceOptions groups independent translations without conflating their error handling.
func composeServiceOptions(app *initApp, fields map[string]*yaml.Node, volumes map[string]bool, owners map[string]string) error {
	var err error
	app.Project.Command, err = composeArgv(fields["entrypoint"], "entrypoint")
	// Entrypoints cannot implicitly introduce a shell or reset an image default with an empty vector.
	if err != nil {
		return err
	}
	app.Project.Args, err = composeArgv(fields["command"], "command")
	// Command strings have image-dependent shell behavior that must be made explicit by the author.
	if err != nil {
		return err
	}
	app.Dependencies, err = composeDependencies(fields["depends_on"])
	// Dependency ordering conditions require a readiness model not inferred from names.
	if err != nil {
		return err
	}
	app.Project.Health, err = composeHealth(fields["healthcheck"], app.Project.Source.Image)
	// Unknown health configuration cannot be replaced by a generic passing check.
	if err != nil {
		return err
	}
	app.Project.Env, app.Project.Secrets, err = composeEnvironment(app.Name, fields["environment"], app.Project.Endpoint.Port)
	// Values are discarded before any candidate can be serialized or printed.
	if err != nil {
		return err
	}
	app.Project.Volumes, err = composeMounts(app.Name, fields["volumes"], volumes, owners)
	// Shared or host-bound storage cannot acquire different ownership silently.
	if err != nil {
		return err
	}
	// Capacity is a reviewable default because Compose does not declare storage capacity.
	if len(app.Project.Volumes) > 0 {
		app.Notes = append(app.Notes, "Named volumes default to 10Gi each; review capacity before deployment.")
	}
	// Every imported environment binding is a reference requiring explicit credential setup.
	if len(app.Project.Secrets) > 0 {
		app.Notes = append(app.Notes, "Environment values were not copied or resolved. Create the named Hostlier secrets before deployment.")
	}
	return nil
}

// composeServicePort folds ports and expose into one unique TCP target.
func composeServicePort(fields map[string]*yaml.Node, image string) (int, error) {
	ports := map[int]bool{}
	// Both lists describe container-side listeners; published ports never become application ports.
	for _, field := range []string{"ports", "expose"} {
		node := fields[field]
		// Omitted lists can still use a known database image's conventional listener.
		if node == nil {
			continue
		}
		// Compose ranges and scalar shortcuts cannot describe a unique listener safely.
		if node.Kind != yaml.SequenceNode || len(node.Content) > 32 {
			return 0, fmt.Errorf("Compose %s must list individual TCP ports", field)
		}
		// Long and short forms share target validation and unsupported-protocol rejection.
		for _, item := range node.Content {
			port, err := composePort(item, field)
			// No valid entry can conceal another unsupported port binding.
			if err != nil {
				return 0, err
			}
			ports[port] = true
		}
	}
	// Known database images have explicit executable readiness defaults.
	if len(ports) == 0 {
		return composeKnownPort(image)
	}
	// Multiple listeners require a deliberate Hostlier endpoint map, not a guessed primary.
	if len(ports) != 1 {
		return 0, fmt.Errorf("Compose service has multiple TCP targets; author explicit Hostlier endpoints")
	}
	// The single map entry is deterministic because uniqueness was checked above.
	for port := range ports {
		return port, nil
	}
	return 0, fmt.Errorf("Compose service needs one TCP port")
}

// composePort parses bounded scalar and long-form port syntax without network access.
func composePort(node *yaml.Node, field string) (int, error) {
	// Long syntax is valid only in ports, never in expose.
	if node.Kind == yaml.MappingNode {
		// Expose does not carry published host-binding objects.
		if field != "ports" {
			return 0, fmt.Errorf("Compose expose requires scalar TCP ports")
		}
		return composeLongPort(node)
	}
	// Scalars contain a target with optional host publication and protocol suffix.
	if node.Kind != yaml.ScalarNode {
		return 0, fmt.Errorf("Invalid Compose TCP port")
	}
	value := strings.TrimSuffix(node.Value, "/tcp")
	parts := strings.Split(value, ":")
	// IPv6 bindings and ranges require explicit endpoint review rather than ambiguous splitting.
	if len(parts) > 3 || strings.ContainsAny(value, "$-/") {
		return 0, fmt.Errorf("Compose supports only individual TCP ports without interpolation")
	}
	return composeShortPort(parts, field)
}

// composeShortPort validates publication metadata independently of the container target.
func composeShortPort(parts []string, field string) (int, error) {
	// Exposed values cannot include host-side bindings.
	if field == "expose" && len(parts) != 1 {
		return 0, fmt.Errorf("Compose expose must contain container ports only")
	}
	// Published ports must also be concrete even though their host routing is reviewed separately.
	if len(parts) > 1 {
		// The penultimate segment always names the published host port.
		if _, err := composePortNumber(parts[len(parts)-2]); err != nil {
			return 0, err
		}
	}
	// A supplied host binding must be a real IP address even though Hostlier reviews publication separately.
	if len(parts) == 3 && net.ParseIP(parts[0]) == nil {
		return 0, fmt.Errorf("Compose host bindings require a literal IP address")
	}
	return composePortNumber(parts[len(parts)-1])
}

// composeLongPort checks every supported host-binding field while retaining only the container target.
func composeLongPort(node *yaml.Node) (int, error) {
	fields, err := composeMapping(node, "ports", []string{"target", "published", "host_ip", "protocol"})
	// Advanced routing modes cannot be silently collapsed into a plain listener.
	if err != nil {
		return 0, err
	}
	// Host publication semantics are validated even though routes are reviewed separately.
	if err := composeHostPublication(fields); err != nil {
		return 0, err
	}
	target := fields["target"]
	// A long-form binding cannot infer its container-side port from its host publication.
	if target == nil || target.Kind != yaml.ScalarNode {
		return 0, fmt.Errorf("Compose ports require a TCP target")
	}
	return composePortNumber(target.Value)
}

// composeHostPublication validates literal TCP host metadata without claiming its old publication.
func composeHostPublication(fields map[string]*yaml.Node) error {
	// UDP and SCTP cannot be routed through Hostlier's supported TCP listener contract.
	if protocol := fields["protocol"]; protocol != nil && protocol.Value != "tcp" {
		return fmt.Errorf("Compose ports support TCP only")
	}
	// Host publications are not retained, but malformed values still fail input review.
	if published := fields["published"]; published != nil {
		// Dynamic ranges have no single publication to review.
		if _, err := composePortNumber(published.Value); err != nil {
			return err
		}
	}
	// Unexpanded host addresses must never be silently interpreted as the local environment.
	if host := fields["host_ip"]; host != nil && (host.Kind != yaml.ScalarNode || net.ParseIP(host.Value) == nil) {
		return fmt.Errorf("Compose host_ip must be a literal address")
	}
	return nil
}

// composePortNumber admits a single nonzero decimal TCP port.
func composePortNumber(value string) (int, error) {
	port, err := strconv.Atoi(value)
	// Reject signs, ranges and values outside the operating system's port interval.
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != value {
		return 0, fmt.Errorf("Compose requires a single TCP port from 1 to 65535")
	}
	return port, nil
}

// composeKnownPort supplies only conventional listeners whose images have known executable probes.
func composeKnownPort(image string) (int, error) {
	// Only recognized official databases have a conventional port inferred from their image.
	switch composeDatabase(image) {
	// Official Redis defaults to its ordinary client listener.
	case "redis":
		return 6379, nil
	// Official PostgreSQL defaults to its ordinary client listener.
	case "postgres":
		return 5432, nil
	// Other images need an explicit listener instead of a guessed web port.
	default:
		return 0, fmt.Errorf("Compose service needs one ports or expose entry")
	}
}

// composeDatabase recognizes only official repository identities, not similarly named custom images.
func composeDatabase(image string) string {
	reference := strings.SplitN(image, "@", 2)[0]
	reference = strings.SplitN(reference, ":", 2)[0]
	reference = strings.TrimPrefix(reference, "docker.io/")
	reference = strings.TrimPrefix(reference, "library/")
	// Custom registries and repository prefixes must provide their own readiness checks.
	if reference == "redis" || reference == "postgres" {
		return reference
	}
	return ""
}

// composeArgv requires explicit literal vectors and preserves argument boundaries.
func composeArgv(node *yaml.Node, field string) ([]string, error) {
	// Omission preserves the image's default entrypoint or command.
	if node == nil {
		return nil, nil
	}
	// Empty vectors reset image defaults in Compose but have no equivalent in the current model.
	if node.Kind != yaml.SequenceNode || len(node.Content) == 0 || len(node.Content) > 128 {
		return nil, fmt.Errorf("Compose %s must be a nonempty argv list; string shell commands and default resets need explicit authoring", field)
	}
	args := make([]string, 0, len(node.Content))
	// Each literal argument stays separate; no shell splitting or variable lookup occurs.
	for _, item := range node.Content {
		value, err := composeArgument(item)
		// Compose interpolation cannot be evaluated without reading secret-bearing environment sources.
		if err != nil {
			return nil, err
		}
		args = append(args, value)
	}
	return args, nil
}

// composeArgument validates one literal vector member without revealing rejected argument bytes.
func composeArgument(item *yaml.Node) (string, error) {
	// Complex and oversized argv entries are not executable process input.
	if item.Kind != yaml.ScalarNode || item.Tag != "!!str" || len(item.Value) > 8192 || strings.ContainsRune(item.Value, 0) {
		return "", fmt.Errorf("Compose argv requires bounded string arguments")
	}
	return composeDollars(item.Value)
}

// composeDollars preserves explicitly escaped runtime dollar signs while rejecting Compose interpolation.
func composeDollars(value string) (string, error) {
	remainder := strings.ReplaceAll(value, "$$", "")
	// The caller must make runtime expansion explicit using Compose's dollar escape.
	if strings.Contains(remainder, "$") {
		return "", fmt.Errorf("Compose interpolation is unsupported in process arguments; use $$ for runtime dollar signs")
	}
	return strings.ReplaceAll(value, "$$", "$"), nil
}

// composeDependencies keeps direct service names without inventing startup-condition semantics.
func composeDependencies(node *yaml.Node) ([]string, error) {
	// A service without dependencies remains an independent candidate.
	if node == nil {
		return nil, nil
	}
	// Long-form readiness, restart and optional-dependency policies need explicit translation.
	if node.Kind != yaml.SequenceNode || len(node.Content) > 64 {
		return nil, fmt.Errorf("Compose depends_on supports a service-name list only; translate conditions explicitly")
	}
	names := map[string]bool{}
	// Duplicate names are rejected so the selection prompt has one unambiguous dependency edge.
	for _, item := range node.Content {
		// Dependency identities must match the same Hostlier-compatible service-name contract.
		if item.Kind != yaml.ScalarNode || !composeNames.MatchString(item.Value) || names[item.Value] {
			return nil, fmt.Errorf("Compose depends_on contains an invalid or duplicate service name")
		}
		names[item.Value] = true
	}
	return composeSortedKeys(names), nil
}

// composeHealth maps executable probes while refusing unsupported scheduling behavior.
func composeHealth(node *yaml.Node, image string) (*model.Health, error) {
	// Recognized databases receive a real readiness probe when Compose did not specify one.
	if node == nil {
		// Each supported database has a real bundled readiness executable.
		switch composeDatabase(image) {
		// Redis readiness uses the bundled client and fails when the server cannot answer.
		case "redis":
			return &model.Health{Exec: []string{"redis-cli", "ping"}}, nil
		// PostgreSQL checks local server readiness without exposing credentials in process arguments.
		case "postgres":
			return &model.Health{Exec: []string{"pg_isready"}}, nil
		}
		return nil, nil
	}
	fields, err := composeMapping(node, "healthcheck", []string{"test"})
	// Interval, timeout, retry and disable semantics cannot be mapped to an overall readiness timeout.
	if err != nil {
		return nil, err
	}
	args, err := composeArgv(fields["test"], "healthcheck.test")
	// Disabled or empty checks must not be replaced with a misleading readiness default.
	if err != nil || len(args) < 2 {
		return nil, fmt.Errorf("Compose healthcheck.test requires CMD or CMD-SHELL and an executable probe")
	}
	// The declared mode selects exact exec arguments or one explicitly authored shell script.
	switch args[0] {
	// Exec probes retain their original argument boundaries.
	case "CMD":
		return &model.Health{Exec: args[1:]}, nil
	// Explicit shell probes are evaluated only by the eventual runtime, never during detection.
	case "CMD-SHELL":
		// Shell probes require a single script rather than an ambiguous joined argument list.
		if len(args) != 2 {
			return nil, fmt.Errorf("Compose CMD-SHELL healthcheck requires one script")
		}
		return &model.Health{Exec: []string{"/bin/sh", "-c", args[1]}}, nil
	// NONE and unknown modes cannot silently disable Hostlier readiness.
	default:
		return nil, fmt.Errorf("Compose healthcheck.test supports CMD or CMD-SHELL only")
	}
}

// composeEnvironment converts destination names to stored references without copying private values.
func composeEnvironment(service string, node *yaml.Node, port int) (map[string]string, map[string]string, error) {
	values, err := composeEnvironmentEntries(node)
	// Unsupported structured values are rejected without printing their bytes.
	if err != nil {
		return nil, nil, err
	}
	env, secrets := map[string]string{}, map[string]string{}
	// Stable names let the user create secrets separately without retaining Compose values.
	for key, value := range values {
		// Bound names so service-name plus environment-name stays within secret-reference limits.
		if !composeEnvNames.MatchString(key) {
			return nil, nil, fmt.Errorf("Compose environment names must be portable identifiers of at most 63 characters")
		}
		// Only fixed runtime mode and the selected listener can be safely retained as public literals.
		if composeSafeEnvironment(key, value, port) {
			env[key] = value
			continue
		}
		secrets[key] = service + "-" + key
	}
	return env, secrets, nil
}

// composeSafeEnvironment permits only explicitly known non-secret process configuration.
func composeSafeEnvironment(key, value string, port int) bool {
	return key == "PORT" && value == strconv.Itoa(port) || key == "NODE_ENV" && (value == "production" || value == "development" || value == "test")
}

// composeEnvironmentEntries validates the two Compose environment forms without external lookups.
func composeEnvironmentEntries(node *yaml.Node) (map[string]string, error) {
	values := map[string]string{}
	// Omitted environment configuration requires neither secrets nor process inspection.
	if node == nil {
		return values, nil
	}
	// Mapping values are retained briefly only to recognize the two safe fixed literals.
	if node.Kind == yaml.MappingNode {
		return composeEnvironmentMap(node)
	}
	// The shorthand is a sequence of NAME or NAME=value strings.
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("Compose environment must be a mapping or list")
	}
	// Splitting once discards no delimiter within a private value.
	for _, item := range node.Content {
		// Sequence values must not be implicitly converted from numbers or complex data.
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return nil, fmt.Errorf("Compose environment list entries must be strings")
		}
		key, value, _ := strings.Cut(item.Value, "=")
		// Duplicate keys need an explicit user decision instead of last-write precedence.
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("Compose environment has duplicate variable names")
		}
		values[key] = value
	}
	return values, nil
}

// composeEnvironmentMap validates scalar destination values without resolving external environment sources.
func composeEnvironmentMap(node *yaml.Node) (map[string]string, error) {
	values := map[string]string{}
	entries, err := composeMapping(node, "environment", nil)
	// Duplicate variable names cannot silently overwrite an earlier secret binding.
	if err != nil {
		return nil, err
	}
	// Structured values are not Compose environment scalars.
	for key, value := range entries {
		// No scalar contents are included in an error message.
		if value.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("Compose environment values must be scalar")
		}
		values[key] = value.Value
	}
	return values, nil
}

// composeDeclaredVolumes accepts only ordinary local named volumes with default driver semantics.
func composeDeclaredVolumes(node *yaml.Node) (map[string]bool, error) {
	volumes := map[string]bool{}
	// An absent volume declaration cannot authorize an undeclared mount later.
	if node == nil {
		return volumes, nil
	}
	entries, err := composeMapping(node, "volumes", nil)
	// Every declaration must be inspectable before service references are resolved.
	if err != nil {
		return nil, err
	}
	// Driver options and external ownership cannot be represented by a managed Hostlier volume.
	for name, declaration := range entries {
		// Hostlier volume identifiers do not support Compose's broader punctuation syntax.
		if !composeNames.MatchString(name) {
			return nil, fmt.Errorf("Compose volume names must use lowercase letters, digits and hyphens")
		}
		// Validate each declaration before any mount acquires its storage ownership.
		if err := composeVolumeDeclaration(declaration); err != nil {
			return nil, err
		}
		volumes[name] = true
	}
	return volumes, nil
}

// composeVolumeDeclaration accepts the local default driver without external names or driver options.
func composeVolumeDeclaration(declaration *yaml.Node) error {
	// Null is the canonical shorthand for a local default volume.
	if declaration.Kind == yaml.ScalarNode && declaration.Tag == "!!null" {
		return nil
	}
	fields, err := composeMapping(declaration, "volume declaration", []string{"driver"})
	// Explicit external, bind-driver and custom naming declarations need deliberate migration.
	if err != nil {
		return err
	}
	// Only the local default driver has managed-volume semantics.
	if driver := fields["driver"]; driver != nil && (driver.Kind != yaml.ScalarNode || driver.Value != "local") {
		return fmt.Errorf("Compose volumes support only the default local driver")
	}
	return nil
}

// composeMounts translates exclusive named-volume mounts and assigns a reviewable capacity default.
func composeMounts(service string, node *yaml.Node, declared map[string]bool, owners map[string]string) (map[string]model.Volume, error) {
	mounts := map[string]model.Volume{}
	// A service with no mounts retains no storage state.
	if node == nil {
		return mounts, nil
	}
	// Mount definitions must be a bounded list rather than an implicit host path.
	if node.Kind != yaml.SequenceNode || len(node.Content) > 128 {
		return nil, fmt.Errorf("Compose volumes must be a bounded list of named mounts")
	}
	// Track ownership globally before independent applications can be selected.
	for _, item := range node.Content {
		source, target, err := composeMount(item)
		// Bind and advanced mount semantics cannot be converted into ordinary persistent volumes.
		if err != nil {
			return nil, err
		}
		// Undeclared or shared storage would change data ownership when split into Hostlier applications.
		if !declared[source] || owners[source] != "" {
			return nil, fmt.Errorf("Compose named volumes must be declared and used by only one service and mount")
		}
		// Paths must already be normalized and cannot mask the entire container root.
		if !composeMountTarget(target) {
			return nil, fmt.Errorf("Compose volume target must be a normalized absolute container path")
		}
		owners[source] = service
		mounts[source] = model.Volume{Mount: target, Size: "10Gi"}
	}
	return mounts, nil
}

// composeMountTarget recognizes normalized absolute container paths without interpreting variables.
func composeMountTarget(target string) bool {
	return strings.HasPrefix(target, "/") && target != "/" && path.Clean(target) == target && !strings.ContainsAny(target, "$\x00\\")
}

// composeMount admits short named mounts and the equivalent explicit volume long form.
func composeMount(node *yaml.Node) (string, string, error) {
	// Long syntax must name a volume instead of a host bind or unsupported mount type.
	if node.Kind == yaml.MappingNode {
		return composeLongMount(node)
	}
	// Scalar mounts require an explicit source and target.
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", "", fmt.Errorf("Compose volumes require named source:target mounts")
	}
	parts := strings.Split(node.Value, ":")
	// Bind paths, anonymous mounts and mode flags beyond plain rw have different persistence semantics.
	if len(parts) < 2 || len(parts) > 3 || !composeNames.MatchString(parts[0]) {
		return "", "", fmt.Errorf("Compose bind and anonymous mounts are unsupported; use a declared named volume")
	}
	// Hostlier's current managed volumes are writable; do not silently discard read-only intent.
	if len(parts) == 3 && parts[2] != "rw" {
		return "", "", fmt.Errorf("Compose mount options other than rw need explicit Hostlier authoring")
	}
	return parts[0], parts[1], nil
}

// composeLongMount validates explicit named storage without dropping mount policy.
func composeLongMount(node *yaml.Node) (string, string, error) {
	fields, err := composeMapping(node, "volume mount", []string{"type", "source", "target", "read_only"})
	// Advanced volume options cannot be silently ignored.
	if err != nil {
		return "", "", err
	}
	// Writable named mounts are the only persistent-storage mode in the existing model.
	if fields["type"] == nil || fields["type"].Value != "volume" || fields["source"] == nil || fields["target"] == nil {
		return "", "", fmt.Errorf("Compose mounts require type volume, source and target; bind mounts are unsupported")
	}
	// Read-only volume semantics need a distinct runtime contract.
	if value := fields["read_only"]; value != nil && (value.Tag != "!!bool" || value.Value != "false") {
		return "", "", fmt.Errorf("Compose read-only mounts require explicit Hostlier authoring")
	}
	return fields["source"].Value, fields["target"].Value, nil
}

// composeSortedKeys gives maps a stable traversal order without reflecting on private values.
func composeSortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	// Keys identify public declarative resources, never copied environment values.
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
