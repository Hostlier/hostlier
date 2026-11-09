# Iteration 3 product contract progress

Implemented durable controller-side policy capture and enforcement across deployment, scale, rollback resolution and restart. Omitting product/workflow metadata cannot shed the captured policy. Explicit policy updates appear in exact deployment review.

Implemented bounded installed-package beforeDeploy / afterSuccess lifecycle phases, per-reviewed-deployment hook keys, original-operation predecessor lookup during recovery, and retention of observed healthy app state when an after-success response fails.

Added parent Hosting SDK `exportHostlierProduct` adapter and branch tests. Node TypeScript build and adapter test passed. No Go controller Node dependency.

Verification: focused Product / Workflow / Scale controller tests and pkg/product/pkg/workflow tests passed, including real installed executable subprocess hooks and restart omission denial. Compiled CLI + Docker PG/Redis E2E is implemented in test/e2e/product_contract_test.go; run status will be appended after completion. The test injects a lost response after a real native after-success hook completes, restarts the controller, rejects forbidden scale through compiled CLI, then inspects and reconciles the exact retained hook.

Final local proof: `HOSTLIER_DOCKER_E2E=1 HOSTLIER_DOCKER_SOCKET="${HOME}/.docker/run/docker.sock" GOCACHE=/tmp/hostlier-product-go-cache go test ./test/e2e -run '^TestDockerProductContract$' -count=1 -v` passed in 13.98 seconds. This compiles the real CLI and native package executables, runs the production controller over its protected Unix API, uses real terminal plan approvals, provisions PG/Redis and executes actual authenticated application write/read containers. A third real PostgreSQL after-success hook completes before an injected lost response. After controller restart, the compiled scale command reaches the server and is denied by the captured policy. Exact hook inspection and explicit reconciliation succeed, with the same three independently observed Docker engine container identities before/after recovery and no additional application execution. Owner-scoped cleanup is automatic.

Final focused Go tests: `go test ./pkg/product ./pkg/workflow ./internal/controller -run 'Product|Workflow|Scale'` passed. Product policy tests include omitted workflow after restart, direct scale API, resource maximums, and a resolved rollback revision that violates retained zero policy. The package subprocess hook journal proves before/after ordering and a fresh hook pair for each new approved deployment. Workflow phase graph tests reject startup references to post-success outputs.

Parent adapter: `npm run build && node --test dist/test/hostlier-product.test.js` passed two tests covering dedicated/shared dependency branches, required secret input references, secret plaintext rejection and explicit unsupported-contract rejection. Adapter deliberately exports only public container products with one or more PostgreSQL provision/database dependencies; private products, native product kinds, runtime/class overrides and table/existing ownership are rejected. It is not a general lossless SDK contract translator.

Quality scan has zero missing comments and no product/workflow complexity violations. Other concurrent feature findings remain owned by their respective changes.

## Final v1 rollback safety verification

The independent journal review found two unsafe rollback handoffs: Docker ignored candidate deletion failure before restarting the source writer; Kubernetes resumed the source after Deployment deletion without observing candidate pod termination. Narrow v1 fixes now fail closed, retain the execution intent, and require confirmed candidate release before prior writer reactivation. Version-two workload group behavior remains separate. Restored-host pending drains clear runtime writer identities only with the protected original-path recovery marker and authoritative absence of both active and source writers; surviving/foreign/unknown sources and corrupt self-references remain blocked, and intentional stopped state is preserved.

Focused deletion/wait-fault and restored-host absence regression tests passed. The entire runtime unit suite and `go vet ./internal/runtime` passed.

Final real Docker rerun against those safety fixes passed with the exact command:

```sh
HOSTLIER_DOCKER_E2E=1 \
HOSTLIER_DOCKER_SOCKET="${HOME}/.docker/run/docker.sock" \
HOSTLIER_CADDY_BINARY=/private/tmp/caddy \
GOCACHE=/private/tmp/hostlier-go-cache \
go test ./test/e2e -run '^TestDocker(Scaling|ScalingProcessCrash|ScalingCapacityFailure)$' -count=1 -v
```

Results: `TestDockerScalingProcessCrash` passed all eight stateless/volume × pull/health/certificate/drain abrupt-exit cases (43.07s); `TestDockerScalingCapacityFailure` passed (4.88s), including capacity and deterministic OOM failure handling; `TestDockerScaling` passed (19.12s), covering resource replacement, retained data, zero, failed wake and approved wake. Entire command: PASS in 67.477s. Fixtures use unique test ownership and owner-scoped cleanup.
