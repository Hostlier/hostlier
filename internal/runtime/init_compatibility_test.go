package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"hostlier/internal/artifact"
	"hostlier/internal/cli"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// initRuntimeProjects exercises the real initializer before passing its desired models through image preparation.
func initRuntimeProjects(t *testing.T) (model.Project, model.Project) {
	t.Helper()
	root := t.TempDir()
	compose := `services:
  web:
    build: .
    expose: [8080]
    depends_on: [cache, db]
    environment:
      NODE_ENV: production
      API_TOKEN: source-token-must-not-ship
  cache:
    image: redis:7
    environment:
      CACHE_TOKEN: source-cache-token
    volumes: [cache-data:/data]
  db:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: source-password-must-not-ship
    volumes: [db-data:/var/lib/postgresql/data]
volumes:
  cache-data: {}
  db-data: {}
`
	// The initializer reads authored inputs but never builds or runs their Docker instructions.
	for path, data := range map[string]string{"compose.yaml": compose, "Dockerfile": "FROM node:24\nEXPOSE 8080\nCMD [\"node\",\"server.mjs\"]\n"} {
		// Keep the source fixture independent of any running engine or cluster.
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	err := cli.RunWithOptions(context.Background(), []string{"init", "--service", "web", "--dependencies", "cache=sidecar,db=app", "--architecture", goruntime.GOARCH}, strings.NewReader(""), &output, io.Discard, cli.Options{CWD: root, Home: t.TempDir()})
	// Backend coverage starts from successful real generated YAML, not a hand-authored approximation.
	if err != nil {
		t.Fatal(err)
	}
	web := readInitRuntimeProject(t, filepath.Join(root, ".hostlier"))
	db := readInitRuntimeProject(t, filepath.Join(root, ".hostlier.db"))
	// Container packaging must be selected before a prepared image replaces the local source contract.
	if web.Source.Container == nil {
		t.Fatal("initializer lost local source")
	}
	web.Source = &model.Source{Image: initRuntimeArchive(t).Image}
	return normalizeInitRuntimeProject(t, web), normalizeInitRuntimeProject(t, db)
}

// readInitRuntimeProject verifies stored secret references survive the initializer's on-disk serialization.
func readInitRuntimeProject(t *testing.T, path string) model.Project {
	t.Helper()
	input, err := config.ReadProjectSources(path, "")
	// Source secret values must not become local upload requests when generated YAML is consumed.
	if err != nil || len(input.Secrets) != 0 {
		t.Fatal("generated config imported literal secrets", err)
	}
	// Both backend inputs must satisfy the same desired-state contract before artifact preparation.
	if err = config.ValidateInput(input.Project); err != nil {
		t.Fatal(err)
	}
	return input.Project
}

// initRuntimeArchive prepares a genuine Linux image transport while preserving a custom application launcher.
func initRuntimeArchive(t *testing.T) artifact.Result {
	t.Helper()
	image, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: goruntime.GOARCH, Config: v1.Config{User: "node", Cmd: []string{"node", "custom-server.mjs"}}})
	// A real image config is required to prove portable archive normalization.
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "custom-app.tar")
	tag, _ := name.NewTag("local/custom-app:test")
	// This models Docker save after a custom recipe build without executing app code or Docker.
	if err = tarball.WriteToFile(path, tag, image); err != nil {
		t.Fatal(err)
	}
	result, err := artifact.PrepareContainer(t.TempDir(), path, goruntime.GOARCH)
	// Both runtimes consume the same prepared immutable archive receipt.
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := artifact.ContainerImage(result.Path, goruntime.GOARCH)
	// Preparation must retain a readable image for the runtime's verified import path.
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := prepared.ConfigFile()
	// Hostlier must never substitute a framework entrypoint for a custom container launcher.
	if err != nil || !reflect.DeepEqual(metadata.Config.Cmd, []string{"node", "custom-server.mjs"}) {
		t.Fatal("custom launcher changed", err)
	}
	return result
}

// normalizeInitRuntimeProject supplies the immutable registry identities normally resolved by controller planning.
func normalizeInitRuntimeProject(t *testing.T, p model.Project) model.Project {
	t.Helper()
	// Locally prepared images are already immutable and use the same representation in both backends.
	if !artifact.IsImage(p.Source.Image) {
		p.Source.Image += "@sha256:" + strings.Repeat("a", 64)
	}
	// Each sidecar has its own image identity rather than borrowing the primary's archive.
	for key, member := range p.Sidecars {
		member.Source.Image += "@sha256:" + strings.Repeat("b", 64)
		p.Sidecars[key] = member
	}
	result, err := config.ValidateProject(p)
	// Runtime renderers receive exactly the normalized project contract used after approval.
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// initManifest selects an actual rendered object while failing clearly for omitted backend capabilities.
func initManifest(t *testing.T, items []map[string]any, kind string) map[string]any {
	t.Helper()
	// Manifest order is significant for policy installation but not object lookup.
	for _, item := range items {
		// Require a rendered object rather than accepting an empty capability set.
		if item["kind"] == kind {
			return item
		}
	} // Select one required rendered object.
	t.Fatalf("missing %s", kind)
	return nil
}

// TestInitGeneratedK3sCompatibility renders generated topology with native sidecars, private access and prepared images.
func TestInitGeneratedK3sCompatibility(t *testing.T) {
	web, db := initRuntimeProjects(t)
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir()})
	// Rendering requires no live Kubernetes cluster or privileged command.
	if err != nil {
		t.Fatal(err)
	}
	k := adapter.(*K3s)
	manifests, err := k.K3sManifests(web, model.DeployContext{OperationID: "init-web", Secrets: map[string]string{"API_TOKEN": "resolved-web", "cache/CACHE_TOKEN": "resolved-cache"}})
	// A generated public app and selected cache must render as one native pod group.
	if err != nil {
		t.Fatal(err)
	}
	assertInitK3sPod(t, web, manifests)
	private, err := k.K3sManifests(db, model.DeployContext{OperationID: "init-db", Secrets: map[string]string{"POSTGRES_PASSWORD": "resolved-db"}})
	// Separate private apps use the same renderer with their own namespace and persistence.
	if err != nil {
		t.Fatal(err)
	}
	assertInitK3sPrivate(t, k, db, private)
}

// assertInitK3sPod verifies that generated app and sidecar settings remain scoped in the rendered pod.
func assertInitK3sPod(t *testing.T, web model.Project, manifests []map[string]any) {
	t.Helper()
	pod := initManifest(t, manifests, "Deployment")["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	primary := pod["containers"].([]any)[0].(map[string]any)
	// Prepared custom-build archives must use the verified local image without registry fallback.
	if primary["image"] != artifact.RuntimeImage(web.Image) || primary["imagePullPolicy"] != "Never" {
		t.Fatal(primary)
	}
	members := pod["initContainers"].([]any)
	// The chosen sidecar shares the pod lifecycle but retains its own readiness and image.
	if len(members) != 1 {
		t.Fatal(members)
	}
	cache := members[0].(map[string]any)
	// The persistent cache must participate in native sidecar startup ordering.
	if cache["restartPolicy"] != "Always" || cache["startupProbe"] == nil || len(cache["volumeMounts"].([]any)) != 1 {
		t.Fatal(cache)
	} // Persistent native sidecars must be ready before the app starts.
	primaryJSON, _ := json.Marshal(primary)
	cacheJSON, _ := json.Marshal(cache)
	// Secret references stay component-qualified and do not leak sibling credentials.
	if !strings.Contains(string(primaryJSON), `"key":"API_TOKEN"`) || strings.Contains(string(primaryJSON), "cache.CACHE_TOKEN") {
		t.Fatal("primary secret scope changed")
	}
	// Member-scoped Kubernetes secret keys must not collapse into the primary scope.
	if !strings.Contains(string(cacheJSON), `"key":"cache.CACHE_TOKEN"`) {
		t.Fatal("sidecar secret scope changed")
	} // Sidecar references use Kubernetes-safe component keys.
	initManifest(t, manifests, "PersistentVolumeClaim")
}

// assertInitK3sPrivate verifies generated dependency consumers constrain cluster ingress and retain database storage.
func assertInitK3sPrivate(t *testing.T, k *K3s, db model.Project, manifests []map[string]any) {
	t.Helper()
	policy := initManifest(t, manifests, "NetworkPolicy")["spec"].(map[string]any)
	ingress := policy["ingress"].([]any)
	// A separate app is private to the selected consumer, never exposed cluster-wide.
	if len(ingress) != 1 {
		t.Fatal(ingress)
	}
	expected := map[string]any{"from": []any{k.namespacePeer("web")}, "ports": []any{map[string]any{"protocol": "TCP", "port": db.Port}}}
	// Private ingress retains exactly the generated consumer and database port.
	if !reflect.DeepEqual(ingress[0], expected) {
		t.Fatal(ingress)
	} // Preserve the exact consumer and database listener.
	deployment := initManifest(t, manifests, "Deployment")
	raw, _ := json.Marshal(deployment)
	// Database exec readiness and mount paths survive without requiring a Node runtime inside its image.
	if !strings.Contains(string(raw), "pg_isready") || !strings.Contains(string(raw), "/var/lib/postgresql/data") {
		t.Fatal(string(raw))
	}
	initManifest(t, manifests, "PersistentVolumeClaim")
}

// initDockerCapture records daemon planning requests without creating containers or connecting to Docker.
type initDockerCapture struct{ bodies []map[string]any }

// request treats inspected resources as absent and records only planned create operations.
func (capture *initDockerCapture) request(request *http.Request) (*http.Response, error) {
	// Read-only probes observe an empty disposable daemon inventory.
	if request.Method == "GET" {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"message":"not found"}`))}, nil
	}
	var body map[string]any
	// Mutation payloads must be valid JSON before any behavioral assertions are meaningful.
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		return nil, err
	}
	body["fixturePath"] = request.URL.Path
	capture.bodies = append(capture.bodies, body)
	return sidecarResponse(map[string]string{"Id": "fixture-created"})
}

// TestInitGeneratedDockerCompatibility constructs the same generated apps with isolated Docker member settings.
func TestInitGeneratedDockerCompatibility(t *testing.T) {
	web, db := initRuntimeProjects(t)
	capture := &initDockerCapture{}
	r := &Runtime{owner: "fixture-owner", options: Options{StateDir: t.TempDir()}, docker: &http.Client{Transport: sidecarTransport(capture.request)}}
	assertInitDockerGroup(t, r, web, map[string]string{"API_TOKEN": "resolved-web", "cache/CACHE_TOKEN": "resolved-cache"})
	assertInitDockerGroup(t, r, db, map[string]string{"POSTGRES_PASSWORD": "resolved-db"})
	// Private app authorization uses the same generated exact consumer list as the K3s policy.
	if !runtimeConsumer(db, "web") || runtimeConsumer(db, "unrelated") {
		t.Fatal("private consumer boundary changed")
	}
	assertInitDockerBodies(t, capture.bodies, web)
}

// assertInitDockerGroup prepares actual owned volumes and member-create requests from a normalized generated app.
func assertInitDockerGroup(t *testing.T, r *Runtime, p model.Project, values map[string]string) {
	t.Helper()
	d := &dockerDeployment{runtime: r, ctx: context.Background(), spec: p, name: p.Name, anchor: p.Name + "-network", dc: model.DeployContext{Secrets: values}}
	// Parent network and storage planning must accept ordinary generated workloads.
	if err := d.prepareVolumes(); err != nil {
		t.Fatal(err)
	}
	// The primary process uses only its own resolved environment and mounts.
	if _, err := d.createMember("", p, d.mounts); err != nil {
		t.Fatal(err)
	} // Primary settings are independent of sidecar settings.
	// Attached dependencies retain member-owned persistence rather than sharing database mounts.
	for key, member := range p.Sidecars {
		mounts, err := d.memberMounts(key, member)
		// Owned storage must exist before a sidecar creation request can be assembled.
		if err != nil {
			t.Fatal(err)
		}
		// Sidecar creation consumes its separately prepared volume list.
		if _, err = d.createMember(key, member, mounts); err != nil {
			t.Fatal(err)
		} // Sidecars use the same concrete builder as normal deployment.
	}
}

// assertInitDockerBodies checks local image identity, readiness, volume mounts and per-member credentials.
func assertInitDockerBodies(t *testing.T, bodies []map[string]any, web model.Project) {
	t.Helper()
	created := 0
	// Resource requests and container requests are deliberately distinguished by their real API path.
	for _, body := range bodies {
		// Network and storage creation records are not process launch requests.
		if body["fixturePath"] != "/containers/create" {
			continue
		} // Only process bodies carry environments and namespace attachment.
		created++
		host := body["HostConfig"].(map[string]any)
		// Processes attach to an owning application namespace instead of publishing independent host ports.
		if !strings.HasPrefix(host["NetworkMode"].(string), "container:") {
			t.Fatal(host)
		} // Members must share only their owning app's namespace.
		encoded, _ := json.Marshal(body)
		assertInitDockerMember(t, body, string(encoded), web)
	}
	// Two primary apps and one explicitly selected sidecar are the complete generated topology.
	if created != 3 {
		t.Fatal("unexpected container count", created)
	}
}

// assertInitDockerMember verifies each component's expected runtime inputs without asserting incidental request ordering.
func assertInitDockerMember(t *testing.T, body map[string]any, encoded string, web model.Project) {
	t.Helper()
	// The locally prepared primary must preserve its image configuration instead of imposing a launcher.
	if body["Image"] == artifact.RuntimeImage(web.Image) {
		assertInitDockerPrimary(t, body, encoded)
		return
	}
	// Database and cache images keep exec readiness plus their own persistent storage.
	if body["Healthcheck"] == nil || !strings.Contains(encoded, `"Type":"volume"`) {
		t.Fatal("dependency readiness or storage missing")
	}
	// The cache must receive its own credential through the selected destination.
	if strings.Contains(encoded, "redis") && !strings.Contains(encoded, "CACHE_TOKEN=resolved-cache") {
		t.Fatal("cache credential missing")
	} // Cache credentials never require the primary's token.
	if strings.Contains(encoded, "postgres") && !strings.Contains(encoded, "POSTGRES_PASSWORD=resolved-db") {
		t.Fatal("database credential missing")
	} // Independent apps receive their own resolved secret destinations.
}

// assertInitDockerPrimary preserves the prepared image launcher and isolates the public app credential.
func assertInitDockerPrimary(t *testing.T, body map[string]any, encoded string) {
	t.Helper()
	// Prepared primary configuration cannot inherit sidecar credentials or an imposed entrypoint.
	if !strings.Contains(encoded, "API_TOKEN=resolved-web") || strings.Contains(encoded, "resolved-cache") || body["Entrypoint"] != nil {
		t.Fatal("primary settings changed")
	}
}
