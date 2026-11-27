# Live K3s swarm validation — 27 November 2026

The remote grouping and permission flow passed on two disposable Ubuntu 24.04
ARM64 Lima VMs, running K3s `v1.34.12+k3s1`. The master had 2 CPUs/2 GiB RAM;
the worker had 2 CPUs/4 GiB RAM. Neither server had Docker installed. The tests
used working-tree binaries, production systemd controllers and CLI commands,
private CA certificates with verification enabled, and isolated signed-in profiles.

| Check | Observed result |
| --- | --- |
| CLI-only controller enrollment | Passed using `hostlier-controller connect` and the signed-in main CLI's `server approve`; no manual fingerprint transfer |
| Outbound connection | Remote operations worked with inbound TCP 7464 blocked on the worker |
| Restricted deployment | An `apps.deploy,apps.stop` grant bound to the remote research group deployed a real Kubernetes workload |
| HTTPS | Both test apps returned the exact submitted echo payload over verified HTTPS |
| Discovery | The viewer saw only research and its permitted app |
| Denials | Cross-group status, logs and deployment were denied; the viewer could not stop apps or create groups; the group manager could not stop apps |
| Lifecycle permissions | The operator could stop its app, could not start it, and the administrator could start it |
| Deployment readiness | Two initial deployments and one replacement served the intended release marker immediately after CLI success, with no retries |
| Rollback readiness | An explicit rollback immediately served the retained release marker; a deploy-only login was denied rollback |
| Start/restart readiness | Four starts and three restarts each served an exact HTTPS echo immediately after CLI success, with no grace period or retries |
| Group moves | Research → private → default preserved the Deployment, Service and Pod UIDs, runtime ID and active revision |
| Old-group access | Status, logs, stop, operation receipt and operation watch access were denied after the move |
| Group deletion | Occupied groups and default could not be deleted |
| Immutable grant scope | Recreating research produced a different group ID; its old viewer grant did not regain access |
| Recovery | Restarting the worker and master preserved grouping, workload identities and permission boundaries |
| Revocation | Revoking the manager's action grant prevented further group creation |
| Cleanup | Both test VMs, application resources and temporary credentials were removed; unrelated VMs were left untouched |

Two production defects surfaced and were fixed before completing the flow:

- Forwarded artifact chunks were subject to the smaller ordinary-request body
  limit. Exact fleet chunk routes now receive the same bounded allowance as
  direct uploads. Regression coverage exercises signed native gRPC through the
  reverse member session and verifies oversized chunks remain rejected.
- CLI device revocation serialized a bodyless action as `null`, which the strict
  controller rejected. It now sends an empty object. A signed CLI-to-controller
  regression verifies the revoked credential loses access.

The inherited fixture also needed explicit installation of the separately built
CLI and an HTTP/2 gRPC localhost proxy with the production 3 MiB request limit.
Those fixture corrections are included in the repeatable runner.

The earlier run observed a transient HTTP 502 immediately after an allowed app
start and temporarily allowed a post-start retry window. That allowance has been
removed. The new run started from fresh VMs and passed all checks above with no
HTTP or transport retries, including three deployment checks, one rollback check,
and seven immediate post-lifecycle HTTPS probes.

Deployments, requested rollbacks, starts and restarts share the same readiness
gate, checking every public HTTP endpoint. CLI rollback resolves the retained
specification and uses the deployment pipeline, including its readiness check.
Legacy routes without a scheme are normalized to HTTPS. Where the primary's
declared HTTP health path is exposed
by its route, readiness requires a 2xx response on that path, without following
redirects. Probes use fresh connections to avoid retiring proxy workers and
require three consecutive successes, 500 ms apart, resetting on failure. This
keeps the operation pending through brief Service/proxy convergence instead of
committing the running state after a single response. TLS verification remains
enabled. Private workloads and gRPC listeners retain their Kubernetes/process
readiness checks; this live fixture exercises HTTP over HTTPS.

After two successful start/restart runs, the expanded deployment/rollback run
began at `2026-11-27T21:54:20Z`. Its controller SHA-256 was
`b2a8047559fcd3eaf9eaf94f6ecac71169854dd298cba5d08d005993e5b35602`;
the laptop CLI SHA-256 was
`1dba01e60ba7aaefdcc8b22129e515c1636a9166c35ba78b9e7152b4cfa6943f`.
Both test VMs and their credentials were removed after success. The nonsecret
run report was written to `/private/tmp/hostlier-deploy-rollback-readiness-report.json`.

Readiness regression tests also pass with the race detector. They cover transient
404/502 responses, stability resetting after failure, unavailable secondary
endpoints, legacy URLs, prefix handling, redirects, untrusted TLS, and fresh
HTTP/1.1 and HTTP/2 connections. Added deployment/rollback tests exercise both
legacy and v2 workloads, hold public health at 502 after Kubernetes rollout
success, and verify the previous runtime revision remains committed until
readiness succeeds. Failed deployment and rollback attempts preserve that
revision. These tests pass under the race detector; controller tests for rollback
revision selection, permissions and policy checks also pass.

The earlier start/restart validation passed the complete runtime test suite and
`go test -p 1 ./...`. The repository-wide check used an isolated temporary
volume to satisfy artifact disk-reserve checks and ran after VM cleanup to avoid
short startup deadlines under concurrent load; the volume was removed afterward.

Run `make e2e-k3s-swarms` to repeat the gate from fresh VMs. See
[setup and scope](../test/e2e/VM_TESTS.md#remote-swarm-grouping-and-permissions-on-live-k3s).
The runner emits a nonsecret JSON report and cleans up on success or failure
unless `--keep-on-failure` is explicitly selected. Physical passkeys, public ACME,
cloud networking, persistent-volume migration and production-cluster upgrades
are separate validation gates.
