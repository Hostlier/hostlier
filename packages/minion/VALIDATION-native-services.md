# Native Minion Fused services validation

Validated locally on 2026-11-02.

## Implemented authoring and packaging

Application code imports `services` from `@fused/minion` and invokes exact selected methods inside Minion actions. The `fused.services` configuration supplies aliases, pinned provider versions and operation lists once. Minion resolves the real Engine contracts, generates provider types and execution bindings, and derives the consent manifest from the same selection. No handwritten provider schema or generated-file import is required in application code.

`minion prepare` generates `.minion/services.d.ts` and `.minion/fused-contract.json` for the app. Build performs preparation and typechecking automatically, then emits the runnable server, `fused-access.json`, and `hostlier-pending.json`. Native startup installs only selected methods before importing the app. User routing and cancellation remain request-local; authenticated connected-user references cannot be overridden by call inputs.

The source-checkout release command is `npm --prefix packages/minion run pack`. It compiles both packages and stages a materialized shared runtime. Direct source-checkout `npm pack`/`npm publish` is guarded because the development dependency symlink otherwise produces parent-relative archive paths.

## Completed checks

- The integrated Minion suite passed 49 tests, covering existing behavior plus native declarations, typechecking, immutable-contract drift, generated packaging and runtime routing.
- Native source build executed a real HTTP action through the local Engine gRPC transport. It returned HTTP 200 with `{"output":{"status":"ok","synthetic":true}}`.
- Generated artifact files contained neither the catalog-reading management key nor the runtime execution token. The test runtime environment omitted the management key.
- The canonical release archive contains 64 entries, including the bundled shared runtime, without parent-relative archive paths.
- A clean, isolated application created its lockfile and ran `npm ci` offline from that tarball. Its installed Minion CLI completed `prepare` and `build`, then its generated action returned the same synthetic response through the actual local Engine.
- Test app processes stopped cleanly. The existing local Engine/Registry stack was retained. No Stripe credentials, provider accounts, payments or external mutations were used.

Local evidence:

- Source-build result: `/private/tmp/minion-native-live-2mK9z0/result.json`
- Source-build artifact: `/private/tmp/minion-native-live-2mK9z0/build/artifact`
- Isolated installed-package result: `/private/tmp/minion-native-install-kJ2unE/verification.json`
- Release archive: `/private/tmp/minion-native-release/fused-minion-0.1.0.tgz`

These are local temporary verification paths, not hosted deployment URLs. Private credential files were not copied into the evidence or archive.

## Deployment authorization status

The managed deployment design uses a controller-only `app.api.deploy` OAuth grant, restricted to one Fused app family and its approved service/operation/bucket ceiling. A stable Hostlier connection name is reused across releases; a new immutable app version receives a new execution-secret reference. The Minion runtime receives execution authority only. Widening the approved ceiling requires renewed consent, and prior release identities remain available for rollback.

The Engine authorization validation is complete:

- The affected Engine OAuth-provider, API and store suites passed: `go test ./internal/engine/oauthprovider ./internal/engine/api ./internal/engine/store`. Database tests skip in this command when no database environment is supplied; they were verified separately below.
- Both real PostgreSQL lifecycle tests passed against isolated schemas: `go test ./internal/engine/store -run 'TestOAuth(Runtime|Deployment)PostgresLifecycle' -count=1 -v`, with the test database configured through a private environment value.
- The deployment grant is restricted to its app family, cannot create unrelated apps, and is rejected by generic management authentication. Refresh tokens are also rejected as bearer authorization.
- New deployments retain older execution tokens for rollback. Refresh preserves the originally approved ceiling. Suspended users lose access, and disconnect revokes the deployment grant and all execution tokens it issued. Execution-token issuance locks the token, consent and client to fence concurrent revocation.
- API tests cover explicit management registration, consent descriptions and rejection of manifest widening. Pre-apply contract checks reject changed immutable identifiers, source hashes or runtime hashes.
- Hostlier's affected CLI, controller, Engine-OAuth and configuration package suites passed.

The native packaging/live execution checks and the database-backed management-lifecycle tests are separate evidence: the existing synthetic execution identity used by the generated app does not itself exercise a new consent ceremony. No hosted OAuth ceremony or public deployment is claimed.

## Real deployment-route verification

The rebuilt local Engine accepted an app-specific disposable test grant at `POST /oauth/runtime/deploy`, ran its real Registry/planner/apply pipeline, and published version `1.0.1` in the original synthetic app family. Its newly issued execution token successfully called `ReadStatus` over Minion gRPC. Requests naming another app, adding an operation, or changing a provider version returned `403 consent_required` and published nothing. The original readiness execution token remained usable.

This check found and fixed two integration gaps: the outer control-authentication router needed an exact POST exception for the dedicated authenticator, and new-version planning needed an internal existing-family authorization anchor rather than workspace-wide app creation. Regression tests cover both; the final Engine command, OAuth provider, API, and store suites pass.

Public receipts are `/private/tmp/fused-minion-local-20261102/deployment-route-result.json` and `deployment-grpc-result.json`. The test grant was seeded only in the disposable local database; this does not claim an exercised hosted browser-consent ceremony. The updated local stack runs under its detached supervisor; its start/stop instructions remain in that directory’s README.txt.

The disposable deployment grant was then revoked through the real runtime revocation endpoint. Further deployment returned 401 and its new runtime token was rejected; the unrelated original readiness token still executed successfully. The test version remains for inspection.


## Caller-selected bucket consent (2026-11-02)

`fused.bucket` is emitted in the exact compiled access manifest and its digest. The Engine renders the authorized selected bucket read-only; omitted bucket configuration retains a searchable picker with keyboard selection and a native no-script fallback. Missing/unauthorized fixed buckets, substituted POST identities, additional buckets and redeployment bucket changes fail closed.

Validation: all 50 Minion tests passed, including native and compatibility build propagation; Hostlier controller/CLI suites passed with bucket-bearing manifests; Engine API/OAuth-provider/command suites passed; two DOM interaction tests passed for search, invalid input, keyboard selection and submitted UUID preservation. Browser testing exercised fallback search/selection and denial with invalid input, then actual fixed-bucket approval from a locally built native Minion artifact, PKCE callback, refresh, same-family deployment, and original/new-release gRPC execution. Other-app deployment, bucket changes and runtime selection of another bucket were rejected. The disposable browser grant was revoked and both runtime credentials subsequently rejected.

Public local receipts: `/private/tmp/fused-minion-local-20261102/browser-consent-fixed-bucket-result.json`, `browser-consent-verification.json`, and `browser-bucket-flow-result.json`. Private credentials are stored separately and were not included in these receipts. The native artifact used `/private/tmp/minion-bucket-flow/minion.json` with `fused.bucket: demo`; no external provider was called.
