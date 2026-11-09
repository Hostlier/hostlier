# Initialize an app

`hostlier init` creates a starter `.hostlier` in the selected directory, including an empty project. Run `hostlier package add NAME` to fill in the installed package’s input definitions. It does not install a server or deploy. Server installation is `hostlier-controller init` on the target Ubuntu server; see [installation](install.md).

Use `hostlier init --package minion` to initialize a Minion in the current directory. Add `--dry-run` to preview the files. An explicit package selection runs that package’s local init hook. Hostlier reports when the package is missing or has no init hook. See the [local package init contract](package-init.md).

```sh
hostlier init
hostlier package add minion
# Fill in packages.minion in .hostlier, then review the workload.
hostlier plan
```

To generate a deployment from existing files, run `hostlier init --detect`. Discovery recognizes authored Dockerfiles, common container build directories, Minion configs, npm apps with a production start script, plain static sites, Docker Compose, and npm/Yarn/pnpm workspace declarations. It uses an existing packaging contract first. For a conventional npm app or static site without one, it creates a starter Dockerfile and a `.dockerignore` if absent. It never executes app scripts during detection or replaces existing deployment files. Guided Minion setup can update its selected build settings in `minion.json`, preserving unrelated fields. Review the generated recipe before building.

Source and deployment flags also select discovery. For example:

```sh
hostlier init --directory apps/web --port 3000
hostlier init --context dist/example --name chat
hostlier init --image registry.example.com/api:1.0.0 --port 8080
hostlier init --route web.example.com --architecture arm64
```

The target defaults to `amd64`; it does not inherit the laptop's CPU architecture. Without a route, normal deployment domain selection remains available. Init reports uncertain ports and any remaining build steps. For Minion, init guides missing build settings and asks for a deployment hostname. It saves the selected target architecture for local image packaging. Minion chooses its pinned Node base image when building the app. Existing values remain unchanged unless explicitly overridden. Headless callers can provide `--route HOST`; safe build defaults need no flags.

`outdir` is optional. Minion automatically uses `build` beside `minion.json`, placing the packaged server, browser assets, Dockerfile and runtime dependencies under `build/artifact`. Set `outdir` only to choose another location. An existing build is preserved; choose another output directory for a later immutable build.

Run `hostlier deploy` in the Minion app directory after setup. Init saves the deployment settings; deploy installs the local framework if missing, builds from source and binds the app identity through Fused consent when required. Preview with `--dry-run` to see proposed public settings without saving them; cancellation also leaves the config unchanged.

## Monorepos

Workspace membership identifies candidates, not permission to deploy everything. Init asks which app to initialize when there is more than one. `--service` takes the discovered relative directory, such as `apps/web`. It follows local runtime dependencies, including through workspace libraries, and ignores `devDependencies` as deployment edges.

For generated npm workspace recipes, the context remains the workspace root, so root lockfiles and local packages are available. Each app gets its own recipe under `.hostlier-build/`. The root lockfile is required. Non-npm workspace packaging needs an authored Dockerfile. Existing Dockerfiles retain their declared contexts; select a different context explicitly when the authored build needs shared files.

## Dependency choices

For every detected runtime dependency, init asks:

- **Skip**: use an existing deployment or configure it later.
- **App**: create an independent `.hostlier` file with its own lifecycle. Dependencies start with private exposure and explicit consumer apps.
- **Sidecar**: put it inside the owning app's `.hostlier` file so it deploys and scales with that app.

Headless callers must make these choices explicitly:

```sh
hostlier init --service apps/web --dependencies apps/api=app,apps/cache=sidecar
hostlier init --service web --dependencies database=app,cache=sidecar
hostlier init --dependencies none
```

`apps` or `sidecars` applies one deliberate placement to the detected dependency graph. Per-dependency choices also accept `skip`. A shared dependency cannot be duplicated into two competing sidecar owners: choose a separate app instead. Private services and sidecars require a real executable readiness probe. Init validates the complete topology before saving any files.

Monorepo configs are saved beside their apps. Compose services sharing a directory use `.hostlier` for the selected app and `.hostlier.SERVICE` for independent dependencies. Deploy each independent app with `hostlier deploy --file PATH`; initialization never deploys these files automatically.

## Docker Compose

Init detects `compose.yaml`, `compose.yml`, `docker-compose.yaml`, or `docker-compose.yml`. Multiple entry files or automatic override files require an explicit reviewed standalone `--compose FILE`. It does not run `docker compose config`, interpolate the shell environment, or read `.env` files.

Supported services use explicit images or local build contexts, one TCP listener, argv commands, declared healthchecks, runtime `depends_on` edges, and local named volumes. Environment variable names become stored `secret://` references rather than copying their values. Set those Hostlier secrets separately. Review the default named-volume capacity reported by init.

Hostlier sidecars share localhost and must have distinct ports. Independently deployed apps require explicit Hostlier endpoint bindings as well as private consumer declarations; configure their connection URLs before deployment. Review inter-service addresses: Compose service DNS names and host-port publication are not preserved automatically. Shared named volumes, bind mounts, external networks, host networking, unsupported build settings, and other settings without a faithful mapping fail with a specific diagnostic instead of disappearing from the result.

For unsupported packaging, keep an explicit Dockerfile or provide an image and author the remaining settings in the generated deployment configuration. Existing deployment configurations, secret files, and build outputs remain untouched; only the selected Minion build settings are updated during guided setup.

## Docker and K3s servers

The generated `.hostlier` model works with both Hostlier runtimes. Init does not choose a runtime: the connected server owns that choice. Install a K3s server with `hostlier-controller init --domain apps.example.com --runtime k3s`, then use the same init, validate, and deploy workflow from your laptop.

Container recipes are built locally into a portable Linux image; Hostlier imports that image into Docker or K3s/containerd. Sidecars share the main app's network namespace on Docker and its pod on K3s. Independent apps keep private consumer declarations. Readiness commands, secret bindings, and named persistent mounts retain their deployment model. A local Docker builder is still required when building a container source for a K3s server. The default K3s local-path storage class records requested PVC size but does not enforce a disk quota.
