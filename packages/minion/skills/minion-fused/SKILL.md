---
name: minion-fused
description: Connect Minion server actions to Fused Engine over gRPC, bind selected SDK/API operations, route authenticated connected users, and diagnose Minion integration failures. Use with @fused/minion rather than Engine-hosted Unified Apps or a standalone Bridge agent.
---

# Connect Minion to Fused

Minion packages selected Fused operations behind its native `services` export. Its shared `EngineTransport` calls the Engine SDK/API service over TLS gRPC; Engine retains permission and credential enforcement.

Minion is maintained and installed by Hostlier: use `hostlier minion install`, then `hostlier minion dev` for local testing. Minion’s existing generator prepares its internal service types and wrappers; application authors do not install a separately generated Fused SDK package.

## Resolve the existing contract

Inspect the app configuration and available Fused connection before asking for missing values. Establish:

- The real gRPC endpoint, which can differ from the Engine HTTP origin.
- The pinned provider services, exact versions and operation names in `fused.services`, or the immutable app UUID for a legacy existing-app build.
- The selected `endpoint_name` and actual input/output contract for each required operation.
- A server-side SDK execution-token reference and any required provider-account connection.

Use the installed `$fused-sdk` skill for operation discovery, app publication and execution-token management, and `$fused-bucket` for provider credentials or account connection when those tasks are needed. If those skills are unavailable, inspect `fused-cli app --help` and service-operation help before invoking management commands. Preserve existing scope and authorization. Do not silently create a new bucket, select all provider operations, mint a token, or substitute another account just to bypass a missing capability.

Read [connections.md](references/connections.md) when selecting operations, binding users or troubleshooting. Prefer `import { services } from '@fused/minion'` and invoke its selected methods inside an active action. Define the service/version/operation selection once in `fused.services`; `hostlier minion prepare` generates the types and `hostlier minion build` prepares and checks them automatically. Do not handwrite provider schemas or import generated files into app code. Build contract reads use the server-only `FUSED_API_KEY` environment; never serialize that key into configuration or artifacts. Existing `defineServices` declarations remain a compatibility path.

For native local development, `FUSED_API_KEY` authorizes contract reads during prepare/build. Execution separately uses `FUSED_ENGINE_GRPC_URL`, `FUSED_APP_ID`, and `FUSED_APP_TOKEN`, supplied through a protected environment. With those already configured, run `hostlier minion prepare`, then `hostlier minion dev`. The dev child excludes `FUSED_API_KEY` and `FUSED_LICENSE_KEY`; never substitute management authority for an execution token. An app without integrations can use just `{ "entry": "app.ts" }` and needs none of these Fused credentials.

## Keep the three credential layers distinct

1. **App session:** authenticates a person to the Minion app. Verify it in `authenticate` and return `{ id, userRef?, serviceUserRefs? }` or `null`.
2. **Engine execution token:** authenticates the backend's runtime calls. Configure it as a Hostlier secret reference or protected server environment value. It is not an Engine management key or a Bridge installation credential.
3. **Provider connection:** stays in Fused's credential bucket and is selected under the published app contract. Do not send raw provider credentials through browser inputs or manufacture connected-user references from them.

Bind connected-user references from the verified app session. `serviceUserRefs` keys match the aliases in `fused.services` (or legacy `defineServices` keys). The runtime isolates requests and rejects per-call user overrides that conflict with a bound user; the app must still prevent untrusted users choosing arbitrary selectors when no authenticated binding exists.

Use the selected SDK's real app version and token. Tokens are SDK-wide in Fused management; the version UUID determines that call's immutable operation scope. Do not promise that a token can access only one version, or revoke a shared token as an incidental cleanup.

## Verify without replaying effects

First test the app's action with an injected fake transport and verify endpoint/input/user routing. For live verification, use an existing authorized runtime identity and a harmless selected read operation, or the mutation explicitly requested by the user. Keep one-time tokens out of tool output, committed files and browser bundles.

`connect()` proves runtime admission, not success of every provider operation. Confirm an actual action result before reporting an integration as working. Preserve the provider's date/freshness metadata. Missing credentials are a runtime setup gap, not a reason to display fabricated successful data.

The transport reconnects after channel loss but does not automatically repeat `Execute`. A failed mutation may already have happened: check its outcome or use the provider's supported idempotency contract before retrying. The action's abort signal is cooperative, not rollback.

Current limits: bounded JSON responses; selected unary `bridge:<alias>` calls and OAuth metadata RPCs are supported. Streaming/binary execution, Engine-hosted Unified App attachments, `fused.db`, worker snapshot/replay, durable webhook receivers and reverse Bridge action serving are separate features. Consult installed package exports if these capabilities change.

## Native packaging and consent

Declare `fused.services` as an alias map of `{ service, version, operations }` in Minion's configuration. Preparation resolves exact Registry contracts into internal `.minion/services.d.ts` and `.minion/fused-contract.json` files; build typechecks the application and emits the matching access manifest and pending deployment artifact. App authors import only Minion. Do not maintain a separate SDK config or provider schema declaration.

Use `fused.engineUrl`, `grpcUrl`, explicit app `version`, and `tokenSecret`. Hostlier's `fused provision` consumes the pending artifact and obtains the authorized Fused identity; runtime credentials remain separate from deployment management authority. `fused bind` can recover a completed grant export. Existing `appId` builds remain supported. The shared transport lives in `@fused/runtime`; `bucketId` may select only an Engine-approved bucket. Expanded scope requires renewed consent; never broaden OAuth scopes just to bypass a failure.

For managed updates, use one stable Hostlier connection name per Minion. The controller's `app.api.deploy` OAuth grant is restricted to that Fused app family and the approved service/operation/bucket ceiling. It stays out of the Minion image and runtime. New releases use an explicit new `fused.version` and a new execution `tokenSecret`; provisioning the same connection publishes within the existing ceiling. Widening the ceiling requires new consent. Confirm the selected Engine/Hostlier versions support this flow before treating it as deployed.
