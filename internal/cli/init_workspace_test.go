package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestInitWorkspaceAbsentLeavesSingleAppDetection checks that ordinary apps retain their existing detector.
func TestInitWorkspaceAbsentLeavesSingleAppDetection(t *testing.T) {
	root := initFixture(t, map[string]string{"package.json": `{"name":"single","scripts":{"start":"node server.js"}}`})
	apps, err := detectWorkspaceApps(root, "amd64")
	// Workspace discovery must not claim a project with no workspace declaration.
	if err != nil || apps != nil {
		t.Fatalf("unexpected workspace result: %#v %v", apps, err)
	}
}

// TestInitWorkspaceRuntimeGraph exposes apps while collapsing only runtime library dependencies.
func TestInitWorkspaceRuntimeGraph(t *testing.T) {
	root := initFixture(t, map[string]string{
		"package.json":                 `{"name":"repo","workspaces":["apps/*","packages/*"],"scripts":{"build":"touch NEVER_RUN"}}`,
		"package-lock.json":            `{}`,
		"apps/web/package.json":        `{"name":"@example/web","scripts":{"start":"node web.js"},"dependencies":{"shared":"workspace:*","alias":"file:../api"},"optionalDependencies":{"worker":"workspace:*"},"devDependencies":{"tool":"workspace:*"}}`,
		"apps/api/package.json":        `{"name":"api","scripts":{"start":"node api.js"}}`,
		"apps/worker/package.json":     `{"name":"worker","scripts":{"start":"node worker.js"}}`,
		"apps/tool/package.json":       `{"name":"tool","scripts":{"start":"touch NEVER_RUN"}}`,
		"packages/shared/package.json": `{"name":"shared","dependencies":{"deeper":"workspace:*"}}`,
		"packages/deeper/package.json": `{"name":"deeper","dependencies":{"api":"workspace:*","shared":"workspace:*"}}`,
		".env":                         "NEVER_READ_PRIVATE_SENTINEL",
	})
	before := initTree(t, root)
	apps, err := detectWorkspaceApps(root, "arm64")
	// Libraries remain metadata; every independently runnable app remains a selection candidate.
	if err != nil || len(apps) != 4 {
		t.Fatalf("apps=%v error=%v", initWorkspaceSortedKeys(apps), err)
	}
	// Runtime and optional edges count; development tools and library-only packages do not become sidecars.
	if !reflect.DeepEqual(apps["apps/web"].Dependencies, []string{"apps/api", "apps/worker"}) {
		t.Fatalf("wrong runtime graph: %v", apps["apps/web"].Dependencies)
	}
	checkInitWorkspaceRecipe(t, root, apps["apps/web"], "apps/web", "npm run build")
	// Detection must not create recipes, execute scripts, or mutate authored private state.
	if !reflect.DeepEqual(before, initTree(t, root)) {
		t.Fatal("workspace discovery modified the source tree")
	}
}

// checkInitWorkspaceRecipe validates root-context installation and an exact selected workspace command.
func checkInitWorkspaceRecipe(t *testing.T, root string, app initApp, relative, build string) {
	t.Helper()
	source := app.Project.Source.Container
	// Shared files and root lockfiles belong to the build context.
	if source.Context != root || !strings.HasPrefix(source.Dockerfile, ".hostlier-build/") {
		t.Fatalf("wrong workspace source: %#v", source)
	}
	recipe := string(app.Files[filepath.Join(root, filepath.FromSlash(source.Dockerfile))])
	// The workspace path is a JSON argument, never interpolated shell source.
	for _, fragment := range []string{"COPY . .", "RUN npm ci", "RUN " + build, fmt.Sprintf(`CMD ["npm","run","start","--workspace",%q]`, relative)} {
		// All critical packaging stages must survive scaffolding.
		if !strings.Contains(recipe, fragment) {
			t.Fatalf("recipe misses %q: %s", fragment, recipe)
		}
	}
	// A child-only Dockerfile would omit sibling libraries and must not also be emitted.
	if _, exists := app.Files[filepath.Join(app.Directory, "Dockerfile")]; exists {
		t.Fatal("generated broken child-only recipe")
	}
}

// TestInitWorkspaceObjectPatternsAndRootApp supports Yarn packages shapes and an independently runnable root.
func TestInitWorkspaceObjectPatternsAndRootApp(t *testing.T) {
	root := initFixture(t, map[string]string{
		"package.json":          `{"name":"root-app","workspaces":{"packages":["apps/*","apps/web"]},"scripts":{"start":"node index.js"}}`,
		"package-lock.json":     `{}`,
		"apps/web/package.json": `{"name":"web","scripts":{"start":"node web.js"}}`,
	})
	apps, err := detectWorkspaceApps(root, "amd64")
	// Overlapping patterns deduplicate one directory while preserving a runnable root candidate.
	if err != nil || !reflect.DeepEqual(initWorkspaceSortedKeys(apps), []string{".", "apps/web"}) {
		t.Fatalf("apps=%v err=%v", initWorkspaceSortedKeys(apps), err)
	}
	checkInitWorkspaceRecipe(t, root, apps["apps/web"], "apps/web", "npm run build --workspaces --if-present")
	rootRecipe := string(apps["."].Files[filepath.Join(root, filepath.FromSlash(apps["."].Project.Source.Container.Dockerfile))])
	// Root startup has no workspace selector and must not accidentally start every child.
	if !strings.Contains(rootRecipe, `CMD ["npm","run","start"]`) {
		t.Fatalf("wrong root command: %s", rootRecipe)
	}
}

// TestInitWorkspacePNPMGlobsSupportsAuthoredContainers exercises recursive membership and exclusions.
func TestInitWorkspacePNPMGlobsSupportsAuthoredContainers(t *testing.T) {
	root := initFixture(t, map[string]string{
		"pnpm-workspace.yaml":             "packages:\n  - 'apps/**'\n  - '!apps/excluded/**'\n",
		"apps/team/web/Dockerfile":        "FROM nginx:1.28\nEXPOSE 8081\n",
		"apps/team/lib/package.json":      `{"name":"lib","scripts":{"build":"touch NEVER_RUN"}}`,
		"apps/excluded/api/Dockerfile":    "FROM nginx:1.28\nEXPOSE 8082\n",
		"node_modules/ignored/Dockerfile": "FROM nginx:1.28\n",
	})
	apps, err := detectWorkspaceApps(root, "amd64")
	// Dockerfiles remain usable in pnpm workspaces without guessing its dependency-manager recipe.
	if err != nil || !reflect.DeepEqual(initWorkspaceSortedKeys(apps), []string{"apps/team/web"}) {
		t.Fatalf("apps=%v err=%v", initWorkspaceSortedKeys(apps), err)
	}
	// Authored container context remains local unless the user deliberately overrides it.
	if apps["apps/team/web"].Project.Source.Container.Context != filepath.Join(root, "apps/team/web") {
		t.Fatal("authored container context changed")
	}
}

// TestInitWorkspaceRejectsAmbiguousOrUnsafeDeclarations keeps invalid monorepos from yielding arbitrary choices.
func TestInitWorkspaceRejectsAmbiguousOrUnsafeDeclarations(t *testing.T) {
	fixtures := map[string]map[string]string{
		"escape":         {"package.json": `{"workspaces":["../other/*"]}`},
		"ambiguous":      {"package.json": `{"workspaces":["apps/../packages/*"]}`},
		"absolute":       {"pnpm-workspace.yaml": "packages: ['/tmp/*']"},
		"invalid-shape":  {"package.json": `{"workspaces":{"nohoist":["*"]}}`},
		"duplicate-name": {"package.json": `{"workspaces":["packages/*"]}`, "packages/a/package.json": `{"name":"duplicate"}`, "packages/b/package.json": `{"name":"duplicate"}`},
		"missing-lock":   {"package.json": `{"workspaces":["apps/*"]}`, "apps/web/package.json": `{"name":"web","scripts":{"start":"node web.js"}}`},
		"pnpm-node":      {"package.json": `{"packageManager":"pnpm@10"}`, "pnpm-workspace.yaml": "packages: ['apps/*']", "apps/web/package.json": `{"name":"web","scripts":{"start":"node web.js"}}`},
		"runtime-escape": {"package.json": `{"workspaces":["apps/*"]}`, "apps/web/Dockerfile": "FROM nginx:1.28\n", "apps/web/package.json": `{"name":"web","dependencies":{"outside":"file:../../../outside"}}`},
	}
	// Every fixture must fail before producing selected deployment metadata.
	for name, files := range fixtures {
		t.Run(name, func(t *testing.T) { // Isolate malformed metadata and preserve the exact original source tree.
			root := initFixture(t, files)
			before := initTree(t, root)
			_, err := detectWorkspaceApps(root, "amd64")
			// No arbitrary candidate may hide a discovery failure.
			if err == nil {
				t.Fatal("unsafe workspace was accepted")
			}
			// Errors must leave authored files intact.
			if !reflect.DeepEqual(before, initTree(t, root)) {
				t.Fatal("failed discovery modified input")
			}
		})
	}
}

// TestInitWorkspaceRejectsSymlinkMembership checks both direct members and symlinked intermediate directories.
func TestInitWorkspaceRejectsSymlinkMembership(t *testing.T) {
	// A matching symlink can escape through either the workspace directory or its parent.
	for _, linked := range []string{"apps", "apps/web"} {
		t.Run(linked, func(t *testing.T) { // Each case owns a separate external target and source tree.
			root := initFixture(t, map[string]string{"package.json": `{"workspaces":["apps/*"]}`})
			target := initFixture(t, map[string]string{"Dockerfile": "FROM nginx:1.28\n", ".env": "PRIVATE"})
			link := filepath.Join(root, linked)
			// Create only the parent of the test symlink.
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			// The target is outside this repository root.
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			_, err := detectWorkspaceApps(root, "amd64")
			// Discovery must reject before inspecting its target.
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("symlink was followed: %v", err)
			}
		})
	}
}

// TestInitWorkspaceRejectsSymlinkMetadata prevents package manifests from becoming arbitrary private-file reads.
func TestInitWorkspaceRejectsSymlinkMetadata(t *testing.T) {
	root := initFixture(t, map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "apps/web/Dockerfile": "FROM nginx:1.28\n", ".env": "PRIVATE_NOT_JSON"})
	// The source is deliberately not valid JSON, so a parser error would reveal a forbidden read.
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(root, "apps/web/package.json")); err != nil {
		t.Fatal(err)
	}
	_, err := detectWorkspaceApps(root, "amd64")
	// Regular-file validation must run before parsing or printing source content.
	if err == nil || !strings.Contains(err.Error(), "regular file") || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("unsafe metadata error: %v", err)
	}
}

// TestInitWorkspacePreservesIgnoreAndNotesUnresolvedRuntime keeps authored policy and unresolved dependencies reviewable.
func TestInitWorkspacePreservesIgnoreAndNotesUnresolvedRuntime(t *testing.T) {
	root := initFixture(t, map[string]string{
		"package.json": `{"workspaces":["apps/*"]}`, "package-lock.json": `{}`, ".dockerignore": "custom-authored-policy\n",
		"apps/web/package.json": `{"name":"web","scripts":{"start":"node web.js"},"dependencies":{"missing":"workspace:*"}}`,
	})
	apps, err := detectWorkspaceApps(root, "amd64")
	// Missing alias targets require review rather than fabricated apps.
	if err != nil {
		t.Fatal(err)
	}
	app := apps["apps/web"]
	// User policy remains authoritative.
	if _, replaced := app.Files[filepath.Join(root, ".dockerignore")]; replaced {
		t.Fatal("authored ignore policy replaced")
	}
	// Both packaging policy and dependency ambiguity must remain visible to the selecting caller.
	for _, text := range []string{"root .dockerignore", "missing"} {
		// Notes cannot disappear merely because a package is runnable.
		if !strings.Contains(strings.Join(app.Notes, "\n"), text) {
			t.Fatalf("missing note %q: %v", text, app.Notes)
		}
	}
}

// TestInitWorkspaceDiscoveryBounds prevents hostile declarations from causing unbounded discovery.
func TestInitWorkspaceDiscoveryBounds(t *testing.T) {
	patterns := make([]string, 129)
	// Repeated patterns must still count toward the parsing-work budget.
	for index := range patterns {
		patterns[index] = "apps/*"
	}
	// Membership expansion is bounded before I/O.
	if _, err := validateInitWorkspacePatterns(patterns); err == nil {
		t.Fatal("pattern bound missing")
	}
	root := initFixture(t, map[string]string{"package.json": `{}`})
	scan := initWorkspaceScan{root: root, patterns: []string{"**"}, directories: map[string]bool{}, entries: initWorkspaceEntryLimit}
	// All visited directory entries count toward the total scan budget.
	if err := scan.walk(".", 0); err == nil {
		t.Fatal("entry bound missing")
	}
	// Recursive patterns cannot exceed the traversal-depth budget.
	if err := scan.walk(".", 17); err == nil {
		t.Fatal("depth bound missing")
	}
}
