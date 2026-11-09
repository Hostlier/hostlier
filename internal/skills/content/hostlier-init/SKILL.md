---
name: hostlier-init
description: Create a starter .hostlier, run an explicitly selected package initializer, or discover an existing app’s deployment settings.
---

# Initialize an app

Run `hostlier init` to create a starter `.hostlier`, then `hostlier package add NAME` to add the installed package’s inputs. Run `hostlier init --package NAME` for its initializer. Use `hostlier init --detect` to infer deployment settings and generate a container recipe from an existing app. It does not install a server, run package scripts, build an image, read `.env` credentials, connect to a controller, or deploy. Existing deployment files are preserved. For Minion, discovery guides missing build settings and atomically saves the selected fields in minion.json while preserving unrelated content. outdir is optional (defaults to build); do not require users to choose it. Pending Fused consent is a guided setup state: run hostlier deploy from the app directory; it installs, builds and provisions the app before deployment review. Preview with `hostlier init --dry-run --json`.

Detection covers explicit Dockerfiles and standard build artifacts, Minion configs, conventional npm start apps, static sites, Docker Compose, and npm/Yarn/pnpm workspace declarations. Package-manager detection does not promise automatic recipes for every tool: unsupported packaging requires an authored Dockerfile. Select a monorepo app by its returned relative directory with `--service`, or choose `--directory` to inspect one app. Choose a Compose service by its service name. Multiple Compose entry files or overrides require an explicit reviewed `--compose FILE`; init never invents merge order.

For each detected runtime dependency, ask whether to skip it, initialize a separate app, or attach it as a sidecar. Never treat every workspace package, devDependency, or unrelated Compose service as a deployment. Libraries remain build dependencies. Noninteractive callers must choose explicitly, for example:

```sh
hostlier init --service web --dependencies cache=sidecar,database=app
hostlier init --directory apps/web --dependencies none --dry-run --json
```

`--dependencies none|apps|sidecars` applies one deliberate mode to the detected graph. A separate app receives its own config and lifecycle; a sidecar is embedded in its owning app's config. Shared dependencies cannot be duplicated into competing sidecar owners. Private apps and sidecars require real executable readiness checks. Port conflicts and unsupported Compose semantics fail before writes.

Hostlier sidecars share a network namespace: use distinct ports and review localhost addressing. Separate apps have their own Hostlier addresses. Compose service DNS, host-port publication, external networks, and arbitrary host mounts are not silently preserved. Environment names become stored secret references, not copied secret values; configure the named secrets separately. Review named-volume size and inter-service URLs before deployment.

Useful overrides: `--name NAME`, `--port PORT`, `--route DOMAIN`, `--architecture amd64|arm64`, `--context DIR`, or `--image IMAGE`. The default target is amd64, not the laptop architecture. Without a route, deployment retains the normal domain-selection workflow. Run `hostlier validate` against the resulting config, complete any reported build/setup steps, then use the ordinary reviewed `hostlier deploy` flow.

Server setup is a separate command: `hostlier-controller init --domain DOMAIN --dry-run`, followed by a reviewed installation on the intended supported Ubuntu host. The `hostlier` CLI contains no server installer.
