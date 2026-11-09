# Native Minion services example

This synthetic example imports `services` directly from `@fused/minion`. Its only provider selection is `fused.services.probe` in `minion.json`; Minion generates the operation wrapper, types and access manifest. The action schema describes the app response rather than re-declaring a provider binding.

The example expects the local test Engine at HTTP port 28081 and gRPC port 15051, with the included `readiness.openapi.json` imported as `local-readiness@1.0.0`. The contract's provider is an explicitly synthetic local `/status` endpoint on port 28082. Supply your build process's catalog-reading credential through `FUSED_API_KEY`; do not put it in this configuration. The checked-in declaration fixture comes from that synthetic contract; preparation replaces it with the selected Engine's real resolved types.

```sh
npx minion prepare minion.json
npx minion build minion.json
```

Preparation writes `.minion/services.d.ts` and `.minion/fused-contract.json`; build does the same automatically and typechecks the app. Include `.minion/services.d.ts` in the application's TypeScript project for editor types. Generated files are internal packaging inputs, never imported by application code.

The pending build is not a deployment. Before deployment replace the example image and route, use an Engine reachable by the target runtime, and authorize the actual app selection through `hostlier fused provision NAME --file build-1.0.0/artifact/hostlier-pending.json`. Keep `NAME` stable across releases and use a new Fused version and execution-secret reference for a new release. Hostlier retains the app-scoped deployment grant and supplies only the runtime identity and execution-token secret to the app. A loopback Engine address is suitable only when the Minion process runs on that same host; it does not reach the laptop from a remote container.

No Stripe account, key, payment or external provider effect is involved.
