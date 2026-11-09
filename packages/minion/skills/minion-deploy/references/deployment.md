# Build and deployment recipe

Minion is installed and managed by Hostlier. Use Node 24+ and the complete Hostlier release, preserving `minion/` beside the CLI binary. From the app directory:

```sh
hostlier minion install
hostlier minion dev
```

Local development can use just `{ "entry": "app.ts" }` with optional `ui.entry`; it needs no Docker or controller. Integrations still require selected Fused configuration and authorized execution credentials. Keep `.minion/framework/` with the app’s package.json and lockfile so npm can reproduce its framework dependency.

Framework contributors can run `./scripts/package-minion.sh dist/minion` from the Hostlier repository root to build the release tarball. Minion does not require the Hosting SDK.
Example `minion.json` (replace deployment values before deploying):

```json
{
  "entry": "app.ts",
  "ui": { "entry": "ui.tsx", "title": "My Minion" },
  "outdir": "build-1.0.0",
  "name": "my-minion",
  "image": "registry.example.com/my-minion:1.0.0",
  "baseImage": "node:24.21.0-bookworm-slim",
  "route": "my-minion.example.com",
  "fused": {
    "engineUrl": "https://engine.example.com",
    "grpcUrl": "https://grpc.example.com",
    "version": "1.0.0",
    "tokenSecret": "my-minion-engine-token-v1",
    "services": {
      "catalog": { "service": "@example/catalog", "version": "v1", "operations": ["GetRecord"] }
    }
  }
}
```

The service identity, version, operation and domains above are illustrative; select the real Registry contract before building. Select an available concrete Node 24+ release/digest for the target deployment. Paths resolve relative to `minion.json`. Omit `ui` for API-only use. If the application has a separate access secret, add a mapping such as `"secrets": { "APP_ACCESS_TOKEN": "my-minion-app-access" }` and implement the corresponding app authentication adapter.

```sh
hostlier minion prepare minion.json
hostlier minion build minion.json
# Output: build-1.0.0/artifact
```

Preparation reads the exact selected contracts using the server-only `FUSED_API_KEY` environment and writes internal `.minion` declarations and contract metadata. Build repeats preparation, typechecks the app, and emits `fused-access.json`, `hostlier-pending.json`, `Dockerfile`, the server bundle, protocol and locked runtime dependencies, plus `public/` when a UI is included. No key is serialized into those files. Run `hostlier fused provision NAME --file build-1.0.0/artifact/hostlier-pending.json` and complete the actual consent flow to obtain the deployable `.hostlier`. Keep `NAME` stable across releases; increment the Fused app version and select a new execution-secret reference for each new version. The controller-only `app.api.deploy` grant allows management within that specific app family and the approved operation/bucket ceiling; widening it requires renewed consent. Existing-app builds with an explicit `appId` remain supported. It runs as a non-root Node user and handles SIGTERM. Build from this artifact directory rather than the entire repository:

```sh
# Example for an ARM64 server; select the actual target platform.
docker buildx build --platform linux/arm64 \
  -t registry.example.com/my-minion:1.0.0 \
  --push build-1.0.0/artifact

hostlier validate --file build-1.0.0/artifact/.hostlier
hostlier plan --file build-1.0.0/artifact/.hostlier --server production
hostlier deploy --file build-1.0.0/artifact/.hostlier --server production
hostlier status my-minion --server production
```

Use `buildx --push` only when publication/deployment is authorized. An ordinary native Docker build is sufficient when it matches the target platform. For a private image, confirm the controller has authorized pull access; laptop registry login alone does not guarantee server access.

Hostlier lowers `fused` into the Engine URL, app ID and `FUSED_APP_TOKEN` secret binding and rejects competing producers. Leave that translation to Hostlier. For the native consent path, Hostlier stores the approved execution token. For a legacy existing-app build, an authorized administrator can provision its existing execution-token secret through the hidden prompt. Runtime execution credentials must remain separate from the app-scoped deployment-management grant; never inject management authority into the Minion container.

## Connect an existing CLI

```sh
hostlier connect https://hostlier.example.com --name laptop
```

The CLI displays a browser review URL. The owner reviews the requested app/secret scope and approves with a browser passkey; the controller shows a one-time code to enter into the initiating CLI. Use the saved profile name reported by the command; the `production` name above is only an example. Browser passkeys need a browser-trusted HTTPS controller origin. Do not modify local DNS/certificate trust merely to make a deployment command pass without authorization for those system changes.

First-passkey enrollment requires local server administration, not a deployment credential:

```sh
sudo hostlier security passkey --name Owner --socket /run/hostlier/controller.sock
```

The owner performs the browser ceremony. Do not treat CLI device keys as a replacement for owner verification.

Scoped credentials currently authorize stateless image deployments to named apps with named secret references and bounded lifetime/use count. They cannot administer clients, upload secrets, add workflows/dependencies or persistent volumes, replace stateful apps, or authorize rollback/resource scaling. Do not strip stateful requirements from the user's requested app to fit a limited grant; use the appropriate owner workflow.

## Verify after deployment

Inspect the operation receipt returned by `deploy`; use the installed CLI's `operation`/`logs` help when resuming observation. Verify the public HTTPS URL and `/healthz`, then call a known read-only app action through its expected app-session flow. For UIs, confirm successful data rendering and useful error behavior. No provider mutation is implied by a request to inspect a deployment.
