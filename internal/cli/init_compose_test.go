package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hostlier/internal/config"
)

// composeFixture writes only declarative input and a harmless Dockerfile for detector tests.
func composeFixture(t *testing.T, document string) string {
	t.Helper()
	root := t.TempDir()
	// The local build context proves path resolution without invoking a container engine.
	if err := os.Mkdir(filepath.Join(root, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	// Detection must not execute this deliberately observable build instruction.
	if err := os.WriteFile(filepath.Join(root, "web", "Dockerfile"), []byte("FROM node:24\nRUN touch DETECTOR_MUST_NOT_EXECUTE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Each test receives a separate document so unsupported cases cannot contaminate another fixture.
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestDetectComposeAppsMapsTopology verifies the actual Hostlier contracts and credential boundary.
func TestDetectComposeAppsMapsTopology(t *testing.T) {
	root := composeFixture(t, `version: "3.8"
services:
  web:
    build: {context: ./web, dockerfile: Dockerfile}
    ports: ["127.0.0.1:8080:3000/tcp"]
    expose: [3000]
    entrypoint: ["node"]
    command: ["server.js", "--literal", "two words"]
    healthcheck: {test: ["CMD", "node", "health.js"]}
    environment:
      PORT: "3000"
      NODE_ENV: production
      API_TOKEN: private-marker-do-not-copy
      DATABASE_URL: "postgres://private-marker-do-not-copy@db/main"
      FROM_SHELL:
    depends_on: [cache, db]
  cache:
    image: redis:7
    environment: ["REDIS_PASSWORD=private-marker-do-not-copy"]
    volumes: [cache-data:/data]
  db:
    image: postgres:17
    expose: [5432]
    healthcheck: {test: ["CMD-SHELL", "pg_isready -U $${POSTGRES_USER}"]}
    environment: {POSTGRES_PASSWORD: private-marker-do-not-copy}
    volumes:
      - {type: volume, source: db-data, target: /var/lib/postgresql/data, read_only: false}
volumes:
  cache-data:
  db-data: {driver: local}
`)
	apps, err := detectComposeApps(root, "compose.yaml", "arm64")
	// The supported topology must become three independently selectable candidates.
	if err != nil {
		t.Fatal(err)
	}
	// Selection order and direct dependency identity are stable and independent of YAML map order.
	if len(apps) != 3 || !reflect.DeepEqual(apps["web"].Dependencies, []string{"cache", "db"}) {
		t.Fatalf("Unexpected candidate dependencies: %#v", apps["web"].Dependencies)
	}
	assertComposeWeb(t, root, apps["web"])
	assertComposeDatabases(t, apps)
	// Every candidate must fit the existing model before the parent chooses its exposure and dependency placement.
	for name, app := range apps {
		// Full model validation checks secret names, sources, volumes, command vectors and health probes together.
		if err := config.ValidateInput(app.Project); err != nil {
			t.Fatalf("Candidate %s is invalid: %v", name, err)
		}
		// Migration notes must explain the semantic difference from Compose DNS, not invent a same-name route.
		if !strings.Contains(strings.Join(app.Notes, " "), "localhost") {
			t.Fatalf("Candidate %s has no shared-namespace note", name)
		}
	}
	encoded, err := json.Marshal(apps)
	// Secret-bearing Compose values never enter serialization, notes or generated files.
	if err != nil || strings.Contains(string(encoded), "private-marker-do-not-copy") {
		t.Fatal("Compose environment value escaped detection boundary")
	}
	// Detection only reads declarations and cannot run Dockerfile commands.
	if _, err := os.Stat(filepath.Join(root, "web", "DETECTOR_MUST_NOT_EXECUTE")); !os.IsNotExist(err) {
		t.Fatal("Detection executed a build instruction")
	}
}

// composeRealPath mirrors canonical source paths without assuming the system temporary directory is not a symlink.
func composeRealPath(t *testing.T, name string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(name)
	// Test fixtures must exist before comparing the detector's confined real paths.
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// assertComposeWeb verifies the source, process and secret contracts without asserting implementation details.
func assertComposeWeb(t *testing.T, root string, app initApp) {
	t.Helper()
	web := app.Project
	// Build inputs are absolute and retain the caller's explicit deployment architecture.
	if web.Source.Container.Context != composeRealPath(t, filepath.Join(root, "web")) || web.Source.Container.Architecture != "arm64" {
		t.Fatalf("Unexpected container source: %#v", web.Source.Container)
	}
	// Published host ports never replace the container socket selected for Hostlier routing.
	if web.Endpoint.Port != 3000 || web.Endpoint.Exposure != "public" {
		t.Fatalf("Unexpected candidate listener: %#v", web.Endpoint)
	}
	assertComposeProcess(t, app)
	// Only two reviewed non-secret values may remain as literal environment entries.
	if !reflect.DeepEqual(web.Env, map[string]string{"PORT": "3000", "NODE_ENV": "production"}) || web.Secrets["API_TOKEN"] != "web-API_TOKEN" || web.Secrets["FROM_SHELL"] != "web-FROM_SHELL" {
		t.Fatalf("Unexpected environment destinations: %#v %#v", web.Env, web.Secrets)
	}
}

// assertComposeProcess verifies explicit process and probe argument boundaries.
func assertComposeProcess(t *testing.T, app initApp) {
	t.Helper()
	web := app.Project
	// Explicit argv and readiness boundaries survive translation without shell splitting.
	if !reflect.DeepEqual(web.Command, []string{"node"}) || !reflect.DeepEqual(web.Args, []string{"server.js", "--literal", "two words"}) || !reflect.DeepEqual(web.Health.Exec, []string{"node", "health.js"}) {
		t.Fatal("Process or probe argv changed")
	}
}

// assertComposeDatabases checks executable readiness and explicitly escaped runtime variables.
func assertComposeDatabases(t *testing.T, apps map[string]initApp) {
	t.Helper()
	// The escaped Compose dollar becomes a runtime shell variable, never a host environment lookup.
	if !reflect.DeepEqual(apps["db"].Project.Health.Exec, []string{"/bin/sh", "-c", "pg_isready -U ${POSTGRES_USER}"}) {
		t.Fatal("CMD-SHELL runtime variable was not preserved")
	}
	// Database defaults include an actual executable readiness check and exclusive storage capacity.
	if apps["cache"].Project.Endpoint.Port != 6379 || apps["cache"].Project.Volumes["cache-data"].Size != "10Gi" || !reflect.DeepEqual(apps["cache"].Project.Health.Exec, []string{"redis-cli", "ping"}) {
		t.Fatal("Redis defaults missing")
	}
}

// TestDetectComposeRejectsUnsupportedSemantics ensures no partial conversion hides meaningful behavior.
func TestDetectComposeRejectsUnsupportedSemantics(t *testing.T) {
	cases := map[string]string{
		"env-file":               "services:\n  web:\n    image: nginx:1\n    expose: [80]\n    env_file: .env\n",
		"network":                "services:\n  web:\n    image: nginx:1\n    expose: [80]\n    networks: [private]\n",
		"network-root":           "services: {web: {image: nginx:1, expose: [80]}}\nnetworks: {private: {internal: true}}\n",
		"bind":                   "services: {web: {image: nginx:1, expose: [80], volumes: ['./data:/data']}}\n",
		"shared-volume":          "services:\n  a: {image: redis:7, volumes: ['data:/data']}\n  b: {image: redis:7, volumes: ['data:/data']}\nvolumes: {data: {}}\n",
		"external-volume":        "services: {web: {image: redis:7, volumes: ['data:/data']}}\nvolumes: {data: {external: true}}\n",
		"driver-options":         "services: {web: {image: redis:7}}\nvolumes: {data: {driver: local, driver_opts: {type: nfs}}}\n",
		"read-only-volume":       "services: {web: {image: redis:7, volumes: ['data:/data:ro']}}\nvolumes: {data: {}}\n",
		"shell-command":          "services: {web: {image: nginx:1, expose: [80], command: 'private-marker-do-not-copy'}}\n",
		"empty-entrypoint":       "services: {web: {image: nginx:1, expose: [80], entrypoint: []}}\n",
		"interpolated-image":     "services: {web: {image: '${private-marker-do-not-copy}', expose: [80]}}\n",
		"interpolated-command":   "services: {web: {image: nginx:1, expose: [80], command: ['echo', '${private-marker-do-not-copy}']}}\n",
		"build-args":             "services: {web: {build: {context: ./web, args: {TOKEN: private-marker-do-not-copy}}, expose: [80]}}\n",
		"missing-dockerfile":     "services: {web: {build: {context: ./web, dockerfile: Dockerfile.production}, expose: [80]}}\n",
		"multiple-ports":         "services: {web: {image: nginx:1, expose: [80, 443]}}\n",
		"udp":                    "services: {web: {image: nginx:1, ports: ['53:53/udp']}}\n",
		"port-range":             "services: {web: {image: nginx:1, ports: ['8000-8005:80']}}\n",
		"missing-port":           "services: {web: {image: example/not-redis:1}}\n",
		"health-timing":          "services: {web: {image: nginx:1, expose: [80], healthcheck: {test: [CMD, check], interval: 10s}}}\n",
		"disabled-health":        "services: {web: {image: redis:7, healthcheck: {test: [NONE]}}}\n",
		"conditional-dependency": "services: {web: {image: redis:7, depends_on: {db: {condition: service_healthy}}}, db: {image: postgres:17}}\n",
		"missing-dependency":     "services: {web: {image: redis:7, depends_on: [missing]}}\n",
		"duplicate-key":          "services: {web: {image: redis:7, image: postgres:17}}\n",
		"duplicate-environment":  "services: {web: {image: redis:7, environment: ['TOKEN=x', 'TOKEN=y']}}\n",
		"alias":                  "services: {web: &base {image: redis:7}, other: *base}\n",
		"multi-document":         "services: {web: {image: redis:7}}\n---\nservices: {db: {image: postgres:17}}\n",
	}
	// Separate fixtures keep each unsupported feature responsible for its own failure.
	for name, document := range cases {
		// Each subtest isolates one unsupported semantic and its diagnostic privacy contract.
		t.Run(name, func(t *testing.T) {
			root := composeFixture(t, document)
			_, err := detectComposeApps(root, "compose.yaml", "amd64")
			// Unsupported behavior must fail instead of creating a plausible but incomplete application.
			if err == nil {
				t.Fatal("Unsupported Compose semantics were silently accepted")
			}
			// Error text must not echo a rejected secret-bearing scalar from source YAML.
			if strings.Contains(err.Error(), "private-marker-do-not-copy") {
				t.Fatalf("Error disclosed private input: %v", err)
			}
		})
	}
}

// TestComposeConfinementAndBounds verifies filesystem boundaries independently of field validation.
func TestComposeConfinementAndBounds(t *testing.T) {
	root := composeFixture(t, "services: {web: {build: ./outside, expose: [80]}}\n")
	outside := t.TempDir()
	// An existing Dockerfile outside the project must not make a symlink context acceptable.
	if err := os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Detection checks the real source path rather than trusting a lexical root prefix.
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	// A symlink traversal cannot select source beyond the project root.
	if _, err := detectComposeApps(root, "compose.yaml", "amd64"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Expected confinement error, got %v", err)
	}
	// A document beyond the byte limit is rejected before YAML parsing or candidate creation.
	if err := os.WriteFile(filepath.Join(root, "large.yaml"), []byte(strings.Repeat(" ", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	// Large inputs cannot consume unbounded decoder resources.
	if _, err := detectComposeApps(root, "large.yaml", "amd64"); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("Expected size error, got %v", err)
	}
}

// TestComposeNestedContextAndLongPort checks document-relative paths and host-independent target mapping.
func TestComposeNestedContextAndLongPort(t *testing.T) {
	root := composeFixture(t, "services: {web: {image: redis:7}}\n")
	nested := filepath.Join(root, "deploy")
	// A selected document can live below the root while referring to a sibling build context.
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	// Explicit Dockerfiles are retained relative to their contained context.
	if err := os.WriteFile(filepath.Join(root, "web", "Dockerfile.custom"), []byte("FROM node:24\n"), 0600); err != nil {
		t.Fatal(err)
	}
	document := "services:\n  web:\n    build: {context: ../web, dockerfile: Dockerfile.custom}\n    ports: [{target: 8080, published: '9000', host_ip: 127.0.0.1, protocol: tcp}]\n    environment: [PORT=8080, API_TOKEN, NODE_ENV=production]\n"
	// The detector must resolve paths beside this file rather than beside its process working directory.
	if err := os.WriteFile(filepath.Join(nested, "compose.yml"), []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	apps, err := detectComposeApps(root, "deploy/compose.yml", "amd64")
	// A bounded long-form publication remains a single concrete container socket.
	if err != nil {
		t.Fatal(err)
	}
	// The custom recipe remains an explicit input rather than silently reverting to Dockerfile.
	if apps["web"].Project.Source.Container.Dockerfile != "Dockerfile.custom" {
		t.Fatal("Custom Dockerfile was discarded")
	}
	// The internal secret name stays independent of the local shell's corresponding value.
	if apps["web"].Project.Source.Container.Context != composeRealPath(t, filepath.Join(root, "web")) || apps["web"].Project.Endpoint.Port != 8080 || apps["web"].Project.Secrets["API_TOKEN"] != "web-API_TOKEN" {
		t.Fatal("Document-relative source or long port mapping changed")
	}
}
