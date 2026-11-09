# Call a selected operation

Select the real service and exact version once in `minion.json`:

```json
"fused": {
  "engineUrl": "https://engine.example.com",
  "grpcUrl": "https://grpc.example.com",
  "version": "1.0.0",
  "tokenSecret": "catalog-runtime-v1",
  "services": {
    "catalog": { "service": "@example/catalog", "version": "v1", "operations": ["GetRecord"] }
  }
}
```

These identities are illustrative; resolve the actual contract before building. `hostlier minion prepare minion.json` generates editor types and `hostlier minion build minion.json` automatically prepares, checks and packages them. Keep the catalog-reading `FUSED_API_KEY` in the process environment. It is not the deployed execution token.

Inside a `defineAction` executor, use the native import:

```ts
import { services } from '@fused/minion';

const record = await services.catalog.GetRecord({ id: input.id });
return { title: record.title };
```

The generated types come from the selected provider schemas. Engine validates provider execution, while Minion validates the app action's own public input/output schemas. Calls require an active action and propagate its authenticated user and abort signal. Server code can pass supported options:

```ts
await services.catalog.GetRecord({ id: input.id }, {
  selector: { resourceId: authorizedResourceId },
  idempotencyKey: stableOperationKey,
});
```

Resolve resource choices and stable operation keys in trusted app logic. Selectors also support `endUserRef`, `authType` and `authName`; they select admitted connections rather than arbitrary credentials. Pagination accepts `{ maxPages: number }` where the selected operation supports it. Do not add mutation retries merely because an idempotency key is available. Legacy handwritten `defineServices` bindings remain supported for existing apps.

## Run the integration locally

Hostlier installs and manages Minion. With the app’s selected Fused configuration and authorized credentials already supplied through the environment:

```sh
hostlier minion install
hostlier minion prepare
hostlier minion dev --port 0
```

`FUSED_API_KEY` is used only for contract preparation. Supply `FUSED_ENGINE_GRPC_URL`, `FUSED_APP_ID`, and `FUSED_APP_TOKEN` separately for execution; they must identify an existing authorized immutable app version with the selected operations. The dev runner strips build management credentials from the app child. It serves only loopback and preserves app authentication. Neither prepare nor dev creates a consent grant or silently issues execution credentials.

Minion’s own generator continues producing the internal types and service wrappers. An app without integration calls can omit `fused` and run from `{ "entry": "app.ts" }` without Engine credentials.

## Runtime identity and origin

Generated startup reads `FUSED_ENGINE_GRPC_URL`, `FUSED_APP_ID`, `FUSED_APP_TOKEN`, `MINION_ORIGIN` and `PORT`. Native Hostlier `fused` configuration produces the first three; the build produces the app origin and port. Avoid defining competing producers for these reserved names.

`EngineTransport` can also be constructed in a custom server entry point from `@fused/minion/grpc`; it exports `connect`, `execute`, `startConnectSession`, `getConnection`, `listConnectionResources` and `close`. Its token belongs only on the server. OAuth resource metadata uses the wire's snake_case fields; `startConnectSession` returns camelCase URL/session fields. Inspect the installed declarations for exact parameter shapes rather than guessing OAuth payload fields.

Engine TLS is required except explicit loopback-IP test endpoints. Keep certificate and hostname verification enabled. Browser same-origin rules apply to the Minion app independently of the Engine channel.

## Diagnose the failing layer

| Symptom | Check |
| --- | --- |
| gRPC connection/TLS failure | Correct native gRPC hostname/port, trusted CA and TLS routing; an Engine HTTP health URL is not proof of gRPC access. |
| Admission/identity rejection | Correct immutable app version, matching SDK execution token, lifecycle state and token expiry/revocation. |
| Operation unavailable | Exact published endpoint and selected scope; generated methods do not widen the approved contract. |
| Provider auth or resource error | Connected account and bucket bindings admitted by that app; compare session-derived user/resource selectors. |
| Action returns 401/403 | The app's own session/authorization and origin checks, not merely its Engine token. |
| Generic action failure after a valid call | Provider response shape, application transformation and output-schema validation; inspect private diagnostics without exposing secrets. |

A custom `createMinionServer` accepts `onError` for private diagnostics. Avoid printing credentials or full provider payloads just to debug a failure. `/healthz` alone is not a live provider probe: generated startup waits for initial Engine admission, but later Engine outages can leave the UI and local health available.
