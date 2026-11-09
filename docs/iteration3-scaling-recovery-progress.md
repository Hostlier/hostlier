# Iteration 3 scaling recovery evidence

Completed 2026-10-29 using native local Docker/Caddy and a disposable Linux k3s VM. Version-2 workload/sidecar acceptance remains separate.

## Implementation

Version-1 runtime adapters now persist a small protected deployment intent before candidate creation or previous-writer shutdown. A subsequent approved deployment removes only the verified uncommitted candidate and restores the committed source, or finishes the retained predecessor drain after a committed cutover. Intent files contain public execution identity, not secret inputs. Invalid ownership, unsafe journal permissions, symlinks and mismatched candidate identity fail closed. Desired zero stays zero during recovery.

Journal removal failure does not roll back committed traffic. Failed candidate removal cannot restart an old persistent-volume writer. Kubernetes rollback waits for candidate pods to disappear before restoring its previous writer. An explicitly restored controller can reconcile a missing active/source pair only when both daemon lookups establish absence at the verified original state path; ownership errors and other daemon failures never establish absence.

Readiness reports observed `APPLICATION_OOM` and `UNSCHEDULABLE` with fixed messages. Unverified causes keep ordinary exit/timeout errors; arbitrary runtime output is not exposed as diagnostics.

## Real execution gates

- Docker: eight hard-exit subprocess cases, stateless and volume-backed, at pull, running-candidate readiness, switched edge routing, and durable activation before source drain. A fresh adapter plus new approved operation recovers one running app and retained data; uncommitted candidates do not leak. Final post-review suite passed in 43.07 seconds.
- Docker: real daemon rejection of an impossible CPU allocation and kernel OOM of a candidate constrained to 16 MiB. The retained source resumes with its old effective limits and healthy HTTPS. Only the test candidate has automatic restart disabled so the first OOM remains terminal. The diagnostic assertion passed in 4.95 seconds.
- Docker: the existing complete scale/zero/failed-wake/approved-wake/data suite passed after the review fixes in 19.12 seconds.
- k3s: real scheduler refusal (`InsufficientCPU`) preserves stateless HTTPS/data; a failed PVC replacement restores the old writer and original served data. Actual desired/ready counts and limits match a subsequently restored scaled revision. Final post-review suite passed in 101.17 seconds.
- k3s: four hard-exit/fresh-runtime cases, stateless and PVC-backed, after candidate Deployment apply and after Service cutover. All passed after rollback safety fixes in 57.23 seconds; interrupted candidates were removed and original data remained readable.
- Installed CLI: both Docker and k3s approved a scale, selected that immutable revision, scaled again, then approved rollback. Actual limits/replicas returned to the selected revision, HTTPS and pre-existing authenticated Redis reads passed, and the fixture was finally restored to its original revision.

Focused runtime regression tests cover journal ownership, retained drain evidence, failed Docker deletion, failed Kubernetes deletion/pod termination, and guarded clean-server recovery. The full runtime race suite and relevant vet checks passed after safety review.

Reproduction: `TestDockerScalingProcessCrash`, `TestDockerScalingCapacityFailure`, `TestDockerScaling`, `TestK3sScalingCapacityAndRollback`, `TestK3sScalingProcessCrash`, and `scaled_rollback_vm.py`. Opt-ins and installed-fixture prerequisites are in [the E2E guide](../test/e2e/README.md).

Final diagnostic-enabled k3s gate passed in 42.03 seconds: the operation returned `UNSCHEDULABLE`, the independent scheduler observation reported `InsufficientCPU`, and 102 source HTTPS/data reads succeeded during denial. The installed fixture verified healthy after testing and its VM was stopped with state retained. Detailed native evidence: [k3s scaling faults](../../docs/iteration3-k3s-scaling-faults.md).
