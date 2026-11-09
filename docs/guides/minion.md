# Minion apps

[All guides](../../GUIDE.md)

Working with a coding agent? `hostlier skills install minion` adds a guide covering
Minion actions, the Fused connection and deployment. `hostlier skills show minion`
prints it.

For a local Minion Docker context, use `source.container.context` in your project
and run `hostlier deploy` from a paired laptop. Hostlier builds, verifies and uploads
the image over the selected HTTPS profile; publishing to a registry and using SSH
are unnecessary. See [local container deployment](container-deploy.md). The
registry-image workflow below remains supported.

[Minion](../../packages/minion/README.md) is Hostlier’s product for teams whose agents build deterministic apps with typed server actions, an optional browser UI, and Fused integrations. Its framework and shared runtime live in this repository. Actions run locally during development and on your Hostlier server after deployment.

## Install and develop locally

In an empty app directory, run `hostlier init --package minion` to create the app and enroll it under `packages.minion` in `.hostlier`. Run `hostlier minion dev` to start it. Add `--ui` to the scaffold command to include a React form. Open the URL printed by `dev` to try it. Call its public greeting action with `curl --json '{"name":"Ada"}' http://127.0.0.1:4317/api/actions/greet`.

Install the complete [Hostlier CLI archive](build.md), keeping its `minion/` directory beside the executable, and install Node.js 24 or newer. In an app directory, run:

```sh
hostlier minion install
hostlier minion dev
```

`dev` reads `minion.json`, installs the bundled framework if needed, and serves a loopback URL on port 4317. It rebuilds and restarts the app when source files change; if a rebuild fails, the last successful version keeps serving. Stop it with Ctrl-C. Select another port with `--port 4500`, or use `--port 0` for an available port. A local Hostlier controller, Docker, and a deployment profile are unnecessary for development.

With `hostlier init --package minion --ui`, the server runs `load.ts` on each page request. The loader calls typed actions in-process and returns JSON data for the default React component in `ui.tsx`. The component receives `{ data, actions }`; after hydration, event handlers call the same methods over same-origin HTTP. Mark actions safe for page loads with `readOnly: true`. Authentication and authorization still apply. Only return data the current visitor may see: loader results are embedded in the HTML. Configure this with `"ui": { "entry": "ui.tsx", "load": "load.ts" }`. Omitting `ui.load` keeps the existing browser-only entry.

A local app can start with `{ "entry": "app.ts" }` in `minion.json`; add `"ui": { "entry": "ui.tsx" }` for a browser UI. Integration calls also need the selected Fused configuration and server-side Engine execution credentials. The development process inherits those credentials from its environment. App authentication remains enabled according to the authored action configuration.

The installed `@fused/minion` dependency supplies the framework. Hostlier keeps its exact package under `.minion/framework/`; retain that archive with `package.json` and the npm lockfile so a fresh checkout can reproduce the install. If the project ignores `.minion/` or `*.tgz`, add exceptions for this framework package. It contains code, not runtime credentials.

Use `hostlier minion prepare` to prepare the app’s selected Fused contracts for editor autocomplete, and `hostlier minion build` to produce its deployment artifact. Each command defaults to `minion.json`; use a positional config path or `--file CONFIG` for a different app.

The [Ollama Chat example](../../examples/ollama-chat/README.md) is a Minion browser app backed by the separate [Ollama agent](../../examples/ollama-agent/README.md). Both examples live in this repository.

For API-only apps, `GET /` returns the app name, an optional public `description` from `minion.json`, and routes for explicitly public actions without authorization hooks. For apps with a UI, `GET /` serves the page.

## Deploy

Install the Minion adapter with `hostlier package install minion --version CATALOG_COMMIT` on the CLI machine before previewing and deploying a source project:

```sh
hostlier plan
hostlier deploy --route customers.example.com
```

Package init already enrolled Minion in `.hostlier`. Planning reports the scoped Fused permissions. Before requesting consent, Hostlier checks that the packaged manifest matches the plan. Existing Minions without a package selection can still use the automatic deployment flow. See the [package hook contract](package-project.md).

A mini app is an ordinary Hostlier workload with a scoped Fused connection:

```yaml
version: 2
name: customers
source:
  image: registry.example.com/customers:1.0.0
endpoint:
  exposure: public
  port: 8080
  route: customers.example.com
health:
  path: /healthz
fused:
  grpcUrl: https://engine.example.com:443
  appId: 11111111-2222-3333-4444-555555555555
  tokenSecret: customers-engine-token
env:
  MINION_ORIGIN: https://customers.example.com
```

Replace example identifiers with the actual immutable Engine SDK/API app version and its execution token. `tokenSecret` is an existing Hostlier secret name. It is not a provider key, Bridge installation credential, or Engine management key.

```sh
hostlier secret set customers-engine-token
hostlier validate
hostlier plan
hostlier deploy
```

Hostlier validates the connection before deployment and resolves the execution token through its normal secret store. Equivalent repeated validation is stable; conflicting environment/secret/workflow bindings fail. Connections are per workload, so a sidecar can declare a different app identity and secret.

From the initialized app directory, run `hostlier deploy` for each release. Deploy installs the project-local Minion compiler if missing, builds fresh, and opens Fused consent when a grant is needed. It reuses matching grants and keeps Hostlier’s normal deployment review. Node 24+, npm, Docker and a configured Hostlier server are required. For private Fused contracts, sign in with fused-cli for the configured Engine or supply FUSED_API_KEY.

For manual packaging, the `hostlier minion build` command produces a Docker context before you choose a hostname. With a route configured it also writes deployment metadata. Minion selects the pinned Node base image unless you specify `baseImage`. Deploy the local context with `source.container.context`, or publish the image first when using `source.image`. The normal Hostlier approval, image pinning, health, restart, rollback, and lifecycle workflows apply. The controller runs no TypeScript compiler and stores no provider credentials.

Engine registration and token management remain Fused management operations. Updating a Hostlier image does not widen its Engine scope. A new Engine selection uses a new version ID. Rollbacks need their captured Engine version and token to remain usable. Token revocation and rotation must be coordinated explicitly; deleting a workload does not revoke a shared token.
