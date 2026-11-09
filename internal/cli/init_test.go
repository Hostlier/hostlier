package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// initFixture writes authored metadata into a disposable project without invoking its scripts.
func initFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "fixture-app")
	// Named fixture roots supply a valid default application identity.
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	// Every fixture path is local to this test's owned app directory.
	for name, value := range files {
		path := filepath.Join(root, name)
		// Nested workspace fixtures use the same private ownership boundary.
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		// Source files must exist before command discovery begins.
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// initOptions fails any accidental controller contact and avoids the user's persisted profiles.
func initOptions(t *testing.T, root string) Options {
	t.Helper()
	// Project scaffolding has no controller authority or remote mutation path.
	client := &fakeClient{reply: func(request call) (any, error) {
		t.Fatalf("init contacted controller: %s %s", request.method, request.path)
		return nil, nil
	}}
	return Options{CWD: root, Home: t.TempDir(), Client: client}
}

// initTree snapshots every authored byte so failed commands and previews can prove they made no changes.
func initTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	// Walk only this fixture and preserve link identity without following links.
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		// Traversal failures must not hide a partially changed tree.
		if err != nil {
			return err
		}
		// Directories contribute no file contents to the snapshot.
		if entry.IsDir() {
			return nil
		}
		// Symlink fixtures must remain links rather than replacing their targets.
		if entry.Type()&os.ModeSymlink != 0 {
			value, err := os.Readlink(path)
			files[path] = "symlink:" + value
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	})
	// A complete snapshot is required before comparison.
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// initReadProject verifies authored YAML remains compatible with the deployment input contract.
func initReadProject(t *testing.T, path string) model.Project {
	t.Helper()
	project, err := config.ReadProjectInput(path)
	// Authored secret references must pass the real parser, not a test-only YAML decoder.
	if err != nil {
		t.Fatal(err)
	}
	// Validation is local and does not build or upload an artifact.
	if err = config.ValidateInput(project); err != nil {
		t.Fatal(err)
	}
	return project
}

// TestProjectInitDryRunDoesNotExecuteOrWrite keeps discovery separate from application lifecycle scripts and secrets.
func TestProjectInitDryRunDoesNotExecuteOrWrite(t *testing.T) {
	root := initFixture(t, map[string]string{
		"package.json": `{"name":"fixture-app","scripts":{"start":"touch SHOULD_NOT_EXIST","build":"touch SHOULD_NOT_EXIST","postinstall":"touch SHOULD_NOT_EXIST"}}`,
		".env":         "DATABASE_PASSWORD=fixture-secret-never-copy\n",
		".env.local":   "TOKEN=second-fixture-secret\n",
	})
	before := initTree(t, root)
	output, err := invoke([]string{"init", "--detect", "--dry-run", "--json"}, "", initOptions(t, root))
	// Preview must succeed without credentials or a reachable controller.
	if err != nil {
		t.Fatal(err)
	}
	var preview struct {
		Files  map[string]string `json:"files"`
		DryRun bool              `json:"dryRun"`
	}
	// The preview must describe concrete files rather than merely acknowledging detection.
	if err = json.Unmarshal([]byte(output), &preview); err != nil {
		t.Fatal(err, output)
	}
	// Node scaffolding must describe deployment, packaging and credential-exclusion files.
	if !preview.DryRun || len(preview.Files) != 3 {
		t.Fatal(preview)
	} // Node scaffolding includes deployment, container and ignore policy.
	// Neither source-tree writes nor lifecycle execution are permitted during discovery.
	if !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("dry-run mutated the app")
	}
	// Environment files are never copied into previews or generated configuration.
	if strings.Contains(output, "fixture-secret-never-copy") || strings.Contains(output, "second-fixture-secret") {
		t.Fatal("preview included a credential")
	}
}

// TestProjectInitNodeScaffoldValidates checks the actual generated deployment and container recipe together.
func TestProjectInitNodeScaffoldValidates(t *testing.T) {
	root := initFixture(t, map[string]string{"package.json": `{"name":"fixture-app","scripts":{"start":"node server.js"},"dependencies":{"next":"1.0.0"}}`, "package-lock.json": "{}", ".env": "PASSWORD=fixture-private-value\n"})
	output, err := invoke([]string{"init", "--port", "4321", "--name", "chosen-app", "--route", "app.example.test", "--architecture", "arm64"}, "", initOptions(t, root))
	// Initialization must complete entirely locally.
	if err != nil {
		t.Fatal(err)
	}
	project := initReadProject(t, filepath.Join(root, ".hostlier"))
	// Explicit app selections override detected defaults without changing CPU architecture implicitly.
	if project.Name != "chosen-app" || project.Endpoint.Port != 4321 || project.Source.Container.Architecture != "arm64" {
		t.Fatal(project)
	}
	// Runtime configuration and the selected endpoint must agree.
	if project.Env["PORT"] != "4321" {
		t.Fatal(project.Env)
	} // Runtime configuration must match the chosen listener.
	assertNodeInitRecipe(t, root, output)
}

// TestProjectInitDockerFinalStagePort preserves an authored Dockerfile while using its runtime-stage listener.
func TestProjectInitDockerFinalStagePort(t *testing.T) {
	docker := "FROM node:24 AS build\nEXPOSE 3000\nRUN touch SHOULD_NOT_EXIST\nFROM nginx:1.28\nEXPOSE 9090/tcp\n"
	root := initFixture(t, map[string]string{"Dockerfile": docker})
	_, err := invoke([]string{"app", "init", "--detect"}, "", initOptions(t, root))
	// The app alias must reach the same initializer without becoming a service selection.
	if err != nil {
		t.Fatal(err)
	}
	project := initReadProject(t, filepath.Join(root, ".hostlier"))
	// Build-stage exposed ports must not change the deployed runtime listener.
	if project.Endpoint.Port != 9090 {
		t.Fatal(project.Endpoint)
	}
	after, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	// Existing packaging contracts remain byte-for-byte owned by the user.
	if err != nil || string(after) != docker {
		t.Fatal("authored Dockerfile changed", err)
	}
	// Reading Docker metadata must never execute a build-stage instruction.
	if _, err = os.Stat(filepath.Join(root, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
		t.Fatal("Docker build instruction executed", err)
	} // Init does not build containers.
}

// TestProjectInitPreservesExistingConfig covers both regular files and links before any scaffold is created.
func TestProjectInitPreservesExistingConfig(t *testing.T) {
	// A link is just as authoritative as an existing regular deployment file.
	for _, linked := range []bool{false, true} {
		// Each collision gets an untouched Node app that would otherwise create multiple files.
		t.Run(map[bool]string{false: "file", true: "symlink"}[linked], func(t *testing.T) {
			root := initFixture(t, map[string]string{"package.json": `{"name":"fixture-app","scripts":{"start":"node server.js"}}`})
			target := filepath.Join(root, ".hostlier")
			// Point the competing output at an existing user file when exercising links.
			if linked {
				other := filepath.Join(root, "existing.yaml")
				// Preserve the link target to detect accidental truncation through the output path.
				if err := os.WriteFile(other, []byte("private existing configuration"), 0600); err != nil {
					t.Fatal(err)
				}
				// The output collision must retain the link itself.
				if err := os.Symlink(other, target); err != nil {
					t.Fatal(err)
				}
				// Regular output files receive the same no-overwrite guarantee.
			} else if err := os.WriteFile(target, []byte("existing configuration"), 0600); err != nil {
				t.Fatal(err)
			}
			before := initTree(t, root)
			_, err := invoke([]string{"init", "--detect"}, "", initOptions(t, root))
			// The initializer must reject any output collision instead of rewriting the file.
			if err == nil || !strings.Contains(err.Error(), "overwrite") {
				t.Fatal(err)
			}
			// Preflight must stop Dockerfile and ignore-file creation too.
			if !reflect.DeepEqual(before, initTree(t, root)) {
				t.Fatal("collision changed existing files")
			}
		})
	}
}

// TestProjectInitRejectsServerFlags gives a migration hint without touching app or server state.
func TestProjectInitRejectsServerFlags(t *testing.T) {
	// Both formerly supported server selectors must be rejected by the project initializer.
	for _, flag := range []string{"domain", "runtime"} {
		// Run each obsolete flag against an otherwise detectable app.
		t.Run(flag, func(t *testing.T) {
			root := initFixture(t, map[string]string{"Dockerfile": "FROM nginx:1.28\nEXPOSE 8080\n"})
			before := initTree(t, root)
			_, err := invoke([]string{"init", "--" + flag, "example"}, "", initOptions(t, root))
			// The error must direct operators to the controller-owned installation command.
			if err == nil || !strings.Contains(err.Error(), "hostlier-controller init") {
				t.Fatal(err)
			}
			// Obsolete installation flags never authorize local output or infrastructure mutation.
			if !reflect.DeepEqual(before, initTree(t, root)) {
				t.Fatal("obsolete flag mutated files")
			}
		})
	}
}

// TestProjectInitValidationPrecedesWrites rejects invalid desired configuration before emitting any scaffold.
func TestProjectInitValidationPrecedesWrites(t *testing.T) {
	root := initFixture(t, map[string]string{"package.json": `{"name":"fixture-app","scripts":{"start":"node server.js"}}`})
	before := initTree(t, root)
	_, err := invoke([]string{"init", "--name", "INVALID NAME"}, "", initOptions(t, root))
	// App identity validation must run before the first exclusive output write.
	if err == nil {
		t.Fatal("invalid identity accepted")
	}
	// Invalid plans cannot leave partially generated files.
	if !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("validation failure created files")
	} // Partial scaffolds must not survive failed validation.
}

// assertNodeInitRecipe checks reproducibility and credential exclusions of the generated build context.
func assertNodeInitRecipe(t *testing.T, root, output string) {
	t.Helper()
	docker, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	// A lockfile selects reproducible dependency installation in the scaffold.
	if err != nil || !strings.Contains(string(docker), "RUN npm ci") {
		t.Fatal(string(docker), err)
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".dockerignore"))
	// Fresh Node scaffolds must exclude both default and environment-specific credential files.
	if err != nil || !strings.Contains(string(ignore), "**/.env\n**/.env.*\n") {
		t.Fatal(string(ignore), err)
	}
	// Success output and generated files never contain source environment values.
	if strings.Contains(output+string(docker)+string(ignore), "fixture-private-value") {
		t.Fatal("secret copied")
	}
}

// TestProjectInitRelativeDirectoryUsesInvocationRoot makes --directory independent of the test process working directory.
func TestProjectInitRelativeDirectoryUsesInvocationRoot(t *testing.T) {
	root := initFixture(t, map[string]string{"app/Dockerfile": "FROM nginx:1.28\nEXPOSE 8080\n"})
	_, err := invoke([]string{"init", "--detect", "--directory", "app"}, "", initOptions(t, root))
	// Relative app selection belongs to the caller's cwd just like the other local CLI commands.
	if err != nil {
		t.Fatal(err)
	}
	initReadProject(t, filepath.Join(root, "app", ".hostlier"))
	// The parent workspace must not receive the selected child's deployment file.
	if _, err = os.Stat(filepath.Join(root, ".hostlier")); !os.IsNotExist(err) {
		t.Fatal("wrote outside selected app", err)
	}
}

// initComposeFixture supplies two explicit runtime dependencies without requiring containers or credentials.
func initComposeFixture(t *testing.T) string {
	t.Helper()
	return initFixture(t, map[string]string{"compose.yaml": `services:
  web:
    image: nginx:1.28
    expose: [8080]
    depends_on: [db, cache]
    environment:
      API_TOKEN: fixture-compose-token
  db:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: fixture-database-password
  cache:
    image: redis:7
`})
}

// TestProjectInitRequiresExplicitAppAndDependencyChoices makes ambiguous discovery and placement non-mutating failures.
func TestProjectInitRequiresExplicitAppAndDependencyChoices(t *testing.T) {
	root := initComposeFixture(t)
	before := initTree(t, root)
	_, err := invoke([]string{"init", "--detect"}, "", initOptions(t, root))
	// A multi-app workspace must not pick whichever service a map happens to return first.
	if err == nil || !strings.Contains(err.Error(), "--service") {
		t.Fatal(err)
	}
	_, err = invoke([]string{"init", "--service", "web"}, "", initOptions(t, root))
	// Selecting the main app does not implicitly authorize its dependencies' deployment ownership.
	if err == nil || !strings.Contains(err.Error(), "--dependencies") {
		t.Fatal(err)
	}
	_, err = invoke([]string{"init", "--service", "unknown", "--dependencies", "none"}, "", initOptions(t, root))
	// Selection keys identify actual discovered services rather than a requested rename.
	if err == nil || !strings.Contains(err.Error(), "Unknown app") {
		t.Fatal(err)
	}
	// Unanswered choices leave the complete project tree unchanged.
	if !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("selection failure wrote files")
	}
}

// TestProjectInitPromptsForEachDependency preserves independent app and sidecar lifecycle decisions.
func TestProjectInitPromptsForEachDependency(t *testing.T) {
	root := initComposeFixture(t)
	before := initTree(t, root)
	options := initOptions(t, root)
	prompts := []string{}
	// Each answer applies to one concrete runtime dependency before any scaffold is written.
	options.Prompt = func(question string, hidden bool) (string, error) {
		prompts = append(prompts, question)
		// Pending choices cannot leave partially initialized application files behind.
		if !reflect.DeepEqual(before, initTree(t, root)) {
			t.Fatal("wrote before all dependency choices")
		}
		// Keep cache with the app and give the database its own independent lifecycle.
		if strings.Contains(question, "dependency cache ") {
			return "sidecar", nil
		}
		// A separately deployed database receives its own private endpoint.
		if strings.Contains(question, "dependency db ") {
			return "app", nil
		} // Sorted dependency names must remain explicit in each prompt.
		t.Fatal("unexpected dependency prompt", question)
		return "", nil
	}
	output, err := invoke([]string{"init", "--service", "web"}, "", options)
	// Every dependency must receive exactly one review, independent of YAML declaration order.
	if err != nil || len(prompts) != 2 {
		t.Fatal(err, prompts)
	}
	assertInitDependencyTopology(t, root)
	// Neither literal Compose values nor external environment values belong in the receipt.
	if strings.Contains(output, "fixture-compose-token") || strings.Contains(output, "fixture-database-password") {
		t.Fatal("receipt exposed credentials")
	}
}

// assertInitDependencyTopology checks separate private apps, loopback sidecars and explicit secret references.
func assertInitDependencyTopology(t *testing.T, root string) {
	t.Helper()
	web := initReadProject(t, filepath.Join(root, ".hostlier"))
	db := initReadProject(t, filepath.Join(root, ".hostlier.db"))
	// Only the deliberately attached cache shares the selected app's lifecycle and loopback network.
	if len(web.Sidecars) != 1 || web.Sidecars["cache"].Endpoint.Exposure != "loopback" {
		t.Fatal(web.Sidecars)
	}
	// Separate runtime dependencies must remain private and authorize only their actual consumer.
	if db.Endpoint.Exposure != "private" || !reflect.DeepEqual(db.Endpoint.Consumers, []string{"web"}) {
		t.Fatal(db.Endpoint)
	}
	// Stored secret names must survive the authored-reference parser without becoming literal secret sources.
	if web.Secrets["API_TOKEN"] != "web-API_TOKEN" || db.Secrets["POSTGRES_PASSWORD"] != "db-POSTGRES_PASSWORD" {
		t.Fatal("secret reference changed")
	}
	assertInitStoredSecrets(t, root)
}

// TestProjectInitNamedDependencyChoicesCanSkip leaves skipped services without generated deployment files.
func TestProjectInitNamedDependencyChoicesCanSkip(t *testing.T) {
	root := initComposeFixture(t)
	_, err := invoke([]string{"init", "--service", "web", "--dependencies", "cache=skip,db=app"}, "", initOptions(t, root))
	// Explicit per-service flags provide the same decisions without an interactive terminal.
	if err != nil {
		t.Fatal(err)
	}
	web := initReadProject(t, filepath.Join(root, ".hostlier"))
	initReadProject(t, filepath.Join(root, ".hostlier.db"))
	// A skipped dependency cannot be attached as a sidecar through transitive defaults.
	if len(web.Sidecars) != 0 {
		t.Fatal(web.Sidecars)
	}
	// Skipped dependencies must not receive their own deployment document.
	if _, err = os.Stat(filepath.Join(root, ".hostlier.cache")); !os.IsNotExist(err) {
		t.Fatal("skipped dependency initialized", err)
	} // Only included standalone apps own a new config file.
}

// assertInitStoredSecrets verifies generated references cannot accidentally import credential values.
func assertInitStoredSecrets(t *testing.T, root string) {
	t.Helper()
	input, err := config.ReadProjectSources(filepath.Join(root, ".hostlier.db"), "")
	// A secret:// reference must never be converted into a new local credential value.
	if err != nil || len(input.Secrets) != 0 {
		t.Fatal("stored reference became literal secret", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".hostlier.db"))
	// YAML needs explicit stored-reference syntax and must omit the source Compose password.
	if err != nil || !strings.Contains(string(data), "secret://db-POSTGRES_PASSWORD") || strings.Contains(string(data), "fixture-database-password") {
		t.Fatal("unsafe generated secret representation", err)
	}
}

// TestProjectInitCancelledDependencyReviewLeavesNoFiles discards partial choices when the final prompt is interrupted.
func TestProjectInitCancelledDependencyReviewLeavesNoFiles(t *testing.T) {
	root := initComposeFixture(t)
	before := initTree(t, root)
	interrupted := errors.New("fixture interrupted review")
	options := initOptions(t, root)
	calls := 0
	// Accept the first dependency but interrupt before the topology can be fully reviewed.
	options.Prompt = func(string, bool) (string, error) {
		calls++
		// The first answer is not authorization to write a partial deployment group.
		if calls == 1 {
			return "sidecar", nil
		}
		return "", interrupted
	}
	_, err := invoke([]string{"init", "--service", "web"}, "", options)
	// Preserve the prompt failure rather than silently defaulting the unfinished choice.
	if !errors.Is(err, interrupted) || calls != 2 {
		t.Fatal(err, calls)
	}
	// Neither the selected app nor previously accepted dependencies may leave files behind.
	if !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("cancelled dependency review wrote files")
	}
}

// TestProjectInitImageRequiresNoSourceRuntime validates a supplied image without inventing a Docker build.
func TestProjectInitImageRequiresNoSourceRuntime(t *testing.T) {
	root := initFixture(t, map[string]string{})
	_, err := invoke([]string{"init", "--image", "registry.example.test/fixture:1.0.0", "--port", "8123"}, "", initOptions(t, root))
	// Explicit image input supports apps without local detector markers.
	if err != nil {
		t.Fatal(err)
	}
	project := initReadProject(t, filepath.Join(root, ".hostlier"))
	// Image and local container source modes must remain mutually exclusive.
	if project.Source.Image != "registry.example.test/fixture:1.0.0" || project.Source.Container != nil || project.Endpoint.Port != 8123 {
		t.Fatal(project.Source, project.Endpoint)
	}
	entries, err := os.ReadDir(root)
	// An image-only app needs only its local deployment document.
	if err != nil || len(entries) != 1 || entries[0].Name() != ".hostlier" {
		t.Fatal(entries, err)
	}
}

// TestProjectInitCreatesNestedScaffolds supports workspace-specific build files without pre-created output directories.
func TestProjectInitCreatesNestedScaffolds(t *testing.T) {
	root := initFixture(t, map[string]string{"app.ts": "authored source"})
	file := filepath.Join(root, ".hostlier-build", "web", "Dockerfile")
	// Nested output ownership is explicit and does not replace authored application files.
	if err := writeInitFiles(root, map[string]string{file: "FROM node:24\n"}); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(file)
	// A complete new build definition must be available after successful initialization.
	if err != nil || string(actual) != "FROM node:24\n" {
		t.Fatal(string(actual), err)
	}
}

// TestProjectInitRejectsAncestorLinksAndRollsBack removes owned parents after a later unsafe output is rejected.
func TestProjectInitRejectsAncestorLinksAndRollsBack(t *testing.T) {
	root := initFixture(t, map[string]string{"app.ts": "authored source"})
	outside := initFixture(t, map[string]string{"nested/keep": "existing external file"})
	link := filepath.Join(root, ".hostlier-build")
	// The leaf parent already exists so checking only that leaf would miss the redirecting ancestor.
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	before, external := initTree(t, root), initTree(t, outside)
	files := map[string]string{filepath.Join(root, ".aaa-owned", "nested", "first"): "new scaffold", filepath.Join(link, "nested", "Dockerfile"): "must not escape"}
	// The lexically first file is written before the unsafe second parent is discovered.
	if err := writeInitFiles(root, files); err == nil {
		t.Fatal("symlinked scaffold parent accepted")
	}
	// Rollback must remove new files and empty parents while preserving authored files and links.
	if !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("failed scaffold left owned files or directories")
	}
	// File-only snapshots cannot detect a newly created empty parent directory.
	if _, err := os.Lstat(filepath.Join(root, ".aaa-owned")); !os.IsNotExist(err) {
		t.Fatal("failed scaffold left owned parent", err)
	} // File-only snapshots cannot detect an empty directory leak.
	// No output may be written through the redirecting ancestor.
	if !reflect.DeepEqual(external, initTree(t, outside)) {
		t.Fatal("scaffold escaped its selected root")
	}
}

// TestProjectInitNumericDirectoryGetsPortableIdentity supplies an app prefix for valid directories beginning with digits.
func TestProjectInitNumericDirectoryGetsPortableIdentity(t *testing.T) {
	root := initFixture(t, map[string]string{"123-reports/Dockerfile": "FROM nginx:1.28\nEXPOSE 8080\n"})
	_, err := invoke([]string{"init", "--detect", "--directory", "123-reports"}, "", initOptions(t, root))
	// Directory-derived names must be valid deployment identities without a mandatory name override.
	if err != nil {
		t.Fatal(err)
	}
	project := initReadProject(t, filepath.Join(root, "123-reports", ".hostlier"))
	// Prefixing retains the user's directory identity rather than replacing it with a generic label.
	if project.Name != "app-123-reports" {
		t.Fatal(project.Name)
	}
}

// TestProjectInitMinionOutputMustStayInsideApp prevents inferred artifact paths from expanding source ownership.
func TestProjectInitMinionOutputMustStayInsideApp(t *testing.T) {
	// Both absolute and parent-relative output paths require an explicit context override.
	for _, outdir := range []string{"../outside", "/tmp/outside"} {
		// Validate through the public initializer so no scaffold can precede source-path review.
		t.Run(outdir, func(t *testing.T) {
			data, _ := json.Marshal(map[string]string{"name": "fixture-app", "outdir": outdir})
			root := initFixture(t, map[string]string{"minion.json": string(data)})
			before := initTree(t, root)
			_, err := invoke([]string{"init", "--detect"}, "", initOptions(t, root))
			// Inferred escaping contexts must produce an actionable explicit-selection error.
			if err == nil || !strings.Contains(err.Error(), "--context") {
				t.Fatal(err)
			}
			// Invalid source ownership never permits a partial deployment document.
			if !reflect.DeepEqual(before, initTree(t, root)) {
				t.Fatal("invalid context wrote files")
			} // Invalid source ownership never creates a deployment file.
		})
	}
}

// TestProjectInitMinionRejectsExternalOutputSymlink checks an existing ancestor when the artifact itself is not built.
func TestProjectInitMinionRejectsExternalOutputSymlink(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": `{"name":"fixture-app","outdir":"dist"}`})
	outside := t.TempDir()
	// The output path is lexically local but actually owned by another directory.
	if err := os.Symlink(outside, filepath.Join(root, "dist")); err != nil {
		t.Fatal(err)
	}
	before := initTree(t, root)
	_, err := invoke([]string{"init", "--detect"}, "", initOptions(t, root))
	// Resolve parents even when dist/artifact does not yet exist.
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatal(err)
	}
	// Reject redirecting build output before creating any scaffold.
	if !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("symlink escape wrote files")
	} // Reject before creating local or external artifacts.
}

// TestProjectInitMinionUnbuiltOutputAndRootAlias accepts a normal future artifact under an explicitly selected root alias.
func TestProjectInitMinionUnbuiltOutputAndRootAlias(t *testing.T) {
	root := initFixture(t, map[string]string{"minion.json": `{"name":"fixture-app","entry":"app.ts","outdir":"dist","route":"fixture.example.com"}`})
	alias := filepath.Join(t.TempDir(), "app-link")
	// Explicitly selecting a project link is legitimate; inferred child links still obey containment.
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	_, err := invoke([]string{"init", "--detect", "--directory", alias}, "", initOptions(t, root))
	// Building the app is not required merely to initialize its deployment source declaration.
	if err != nil {
		t.Fatal(err)
	}
	project := initReadProject(t, filepath.Join(root, ".hostlier"))
	// The authored source stays portable after the absolute root alias is resolved.
	if project.Source.Container.Context != "dist/artifact" {
		t.Fatal(project.Source.Container)
	}
}
