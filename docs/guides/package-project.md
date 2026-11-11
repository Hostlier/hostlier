# Project package hooks

Hostlier packages may provide any of the local `init`, `project-plan`, `project-package`, and `project-contribute` hooks. Server packages may also provide `project-deploy`. Choose the hooks the package needs. A provider of a shared resource can keep using its existing provisioning workflow without any project hooks.

Use `hostlier package install NAME --file BINARY` to make a binary available on the CLI machine. From the application directory, run `hostlier init` to create the starter manifest, then use `hostlier package add NAME` to enroll it in this project. When installed locally, the package’s `describe` contract supplies the fields under `packages.NAME` in `.hostlier`. Add works offline and does not plan, build or provision anything. Adding an uninstalled package fails without changing the manifest. Repeated adds preserve existing values. Use `--state-dir DIR` to select a local installation directory. Other installed packages stay idle.

```sh
hostlier init
hostlier package add harnest
hostlier plan
hostlier deploy --server production
```

## From a package definition to a deployment

For example, install the PostgreSQL package on the CLI machine and on the controller where it will run. In an existing v2 app project, run `hostlier package add postgres`. The generated fields show which inputs are required, their types, choices and defaults. To provision a new instance and give your app its connection URL, edit the entry like this (unused optional fields may be removed):

```yaml
packages:
  postgres:
    config:
      mode: new-instance
    bindings:
      DATABASE_URL: url
```

`hostlier plan` now shows a workflow step named `postgres` and the binding `DATABASE_URL: postgres.url`. `hostlier deploy --server production` uses the normal controller review and approval. For modes that need credentials, `packages.NAME.secrets` maps a declared alias to an existing **stored secret name**. Put only public values in `config`. Output names are shown as choices; their values are produced at execution, and only outputs the package returns for application use can populate app environment variables. Continue using explicit `workflow.steps` for multiple instances of one package, upstream private inputs, or custom step ordering.

For a package with a local planner, packager or contributor, put its public fields directly under `packages.NAME`. For example, a builder declaring a string `region` receives `packages.builder.region` in the `config` object of its `project-plan`, `project-package` and `project-contribute` requests. Hostlier validates settings for all enrolled packages before invoking any of those hooks. A builder can emit ordinary workflow steps for its controller-side work.

For a Minion app, run `hostlier package add minion`, set `packages.minion.name`, and point `entry` at your server source (for example, `src/service.ts`). Set `baseImage` to override the Node image. Hostlier derives the local container source and default HTTP endpoint. Projects with `minion.json` can keep using it for build defaults, UI configuration and Fused permissions.

For a new Harnest agent, run `hostlier package add harnest`, set `packages.harnest.name`, then run `hostlier init --package harnest`. The initializer creates the same minimal managed ADK agent as Harnest’s own starter. Use `--dry-run` to preview it. Set `baseImage` to a digest-pinned Linux image or multi-platform index. With an index, the same file can target amd64 and arm64. A server-targeted plan or deployment asks its controller for the Linux architecture. For an offline preview use `hostlier plan --architecture arm64` or `--architecture amd64`. Docker and K3s verify registry image platforms before approving the workload. On a matching Linux host with the Harnest compiler installed, packaging can compile the source. On another platform, set `artifact` to a project-relative executable compiled for the target Linux architecture. `requiredBindings` lists names the deployment must supply. Harnest reads its build settings from `packages.harnest` in `.hostlier`.

**For package authors:** publish a v1 `describe` capability. Use `configSchema.properties` for your top-level public fields, `required` for mandatory fields, and property `description`, `enum` and `default` to guide the operator. The configuration must be an object; use `additionalProperties: false` to reject typos. Blank top-level scaffold fields mean unset; Hostlier applies top-level defaults and validates the resulting object against the schema, including nested constraints. Use a self-contained JSON Schema: external file and network references are not loaded. A local hook with an empty legacy schema keeps its original request format. For controller packages, declare `secretAliases`, an `outputSchema`, and optionally an explicit list of bindable names in `outputs`. When that list is omitted, Hostlier uses the names in `outputSchema.properties`. The same metadata is visible with `hostlier package describe NAME`. Go workflow packages built with `packagekit.Define` already publish these fields through `Capability()`; other executables can emit the JSON directly.

A project can enroll one primary workload planner or packager and several local contributors. The older `package: NAME` selector still enrolls that package. A package advertises `project-contribute` to add infrastructure. It receives the v1 project planning request and returns one JSON document, for example:

```json
{"version":1,"dependencies":{"cache":{"type":"redis","source":"provision","storage":"1Gi"}},"bindings":{"CACHE_URL":"cache.url"}}
```

Contributors can also return `steps` and `workflowBindings`. The CLI calls enrolled contributors in package-name order. Identical named declarations are coalesced, and conflicting definitions fail planning. The resulting workflow uses ordinary step IDs and `needs`. The builder receives contributed output names in the optional `providedBindings` request field. An enrolled controller package contributes a workflow step from its declared config, secrets and output bindings. A single package can both build and contribute.

Contributors may add `sidecars` using the ordinary v2 sidecar schema and `secrets` mapping parent environment names to **stored secret names**. These require a CLI with sidecar contribution support; older CLIs reject the unknown fields. A package does not receive resolved secrets. Parent secret destination names are included in `providedBindings`, alongside URL and workflow binding names. For example:

```json
{
  "version": 1,
  "sidecars": {
    "browser": {
      "source": {"image": "registry.example.com/team/browser:1"},
      "endpoint": {"exposure": "loopback", "port": 9222},
      "secrets": {"SANDBOX_TOKEN": "browser-token"},
      "health": {"exec": ["node", "/opt/hostlier-sandbox/health.mjs"]},
      "required": true
    }
  },
  "bindings": {"BROWSER_URL": "sidecars.browser.url"},
  "secrets": {"BROWSER_TOKEN": "browser-token"}
}
```

Composition rejects conflicting sidecars and secret references, including conflicts with the authored project. The combined project still passes ordinary validation: sidecars cannot publish routes, nest deployments, or reuse another component's declared port. Their images, scoped secrets, security policies and health probes participate in the existing deployment approval, artifact preparation, rollback and lifecycle. A sidecar shares its parent's network namespace; it is part of the same trusted application group, not a separate network sandbox. See [browser and computer sidecars](sandbox-sidecars.md) for the supplied packages.

Local package planning proposes the deployment spec. Deployment plans again using the current source, packages it, and uses the controller’s ordinary deployment review. The controller approval covers the resolved image and controller workflow. See the [local package discovery rules](package-init.md) and the [controller deploy hook](package-deploy.md).

## Executable contract

The executable advertises its supported entry points in `describe`, for example `"hooks":["project-plan","project-package"]`. Hostlier runs each hook as the sole command-line argument with the project root as its working directory. It sends one JSON document on stdin and reads one JSON document on stdout. Each hook runs in its own process. During one deployment Hostlier uses the same executable snapshot for both calls.

### project-plan

Read the project and declare its requirements. Use configuration or static analysis for this stage. Do not run application code, build images or provision resources. Input:

```json
{"version":1,"projectDir":"/work/example","architecture":"arm64"}
```

Return a Hostlier v2 project. Declare dependencies, named secret references, workflow steps, and bindings using Hostlier’s normal project schema. Use `source.container.context: "."` for the root of the context the package will produce. An optional public `declarations` value can carry additional package-specific permissions or build decisions.

```json
{
  "version": 1,
  "project": {
    "version": 2,
    "name": "example",
    "source": {"container":{"context":".","architecture":"arm64"}},
    "endpoint": {"exposure":"public","port":8080}
  },
  "declarations": {"catalogOperations":["read"]}
}
```

### project-package

Build from the same project for the selected Linux architecture. The request adds the selected project, the declarations (if any), and an absolute, empty output directory:

```json
{
  "version": 1,
  "projectDir": "/work/example",
  "architecture": "arm64",
  "outputDir": "/tmp/hostlier-build-123",
  "project": {
    "version": 2,
    "name": "example",
    "source": {"container":{"context":".","architecture":"arm64"}},
    "endpoint": {"exposure":"public","port":8080}
  },
  "declarations": {"catalogOperations":["read"]}
}
```

Write the Docker context into that directory and return its relative path:

```json
{"version":1,"artifact":{"kind":"container-context","path":"."}}
```

Hostlier builds and transfers the image using the normal artifact workflow. A package may return a subdirectory. Hostlier rejects a missing directory, a path that escapes the output tree, and an unsupported artifact kind. If the intended permissions or dependencies changed since planning, return an error before producing the artifact.

### Process environment and errors

The local hooks receive a temporary home directory and an executable search path. Packaging also receives `HOSTLIER_CLI`, the executable for the invoking client. A package may declare an app environment variable such as `"projectOriginEnv":"APP_ORIGIN"` in its capability. Hostlier sets it to the selected public origin, including when the operator overrides the route.

A package can advertise additional build-time environment variable names in `describe.projectPackageEnv`. The operator must also list the allowed names in `HOSTLIER_PACKAGE_ENV` (comma separated). Planning receives no additional build-time values. These local processes run with the user's filesystem permissions.

A public diagnostic is a successful process exit with `{"version":1,"error":"Project declarations changed. Run plan again."}`. Use a nonzero exit to report a general hook failure. Keep secret values out of plans, declarations, output and error messages. Local hook documents are bounded to 4 MiB. Hostlier allows one minute for planning and 15 minutes for packaging.
