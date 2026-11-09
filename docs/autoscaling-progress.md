# Native CPU autoscaling implementation progress

Implementation and local verification, 2026-10-29. The approved `.hostlier` deployment may now declare explicit `requests` and an `autoscaling` policy (`minReplicas`, `maxReplicas`, `cpuTargetPercent`). This delegates to a native autoscaling/v2 HPA with +1/60s, -1/300s and a 300-second downscale stabilization window. Requests and limits must explicitly declare CPU and memory; v1 stateless k3s only. Maximum replicas are checked against retained ProductPolicy. Existing deployment interactive approval previews bounded capacity, request denominator, revocation and persistence.

Manual scale and rollback remove delegation from proposed state. Runtime deployment handoff and lifecycle actions durably pause the prior HPA and delete it before capacity mutations. A newly approved HPA is installed only after target readiness, HTTPS cutover and a durable pending-install journal containing Deployment UID and policy fingerprint. Existing HPA replica ownership is preserved on immutable operation replay. Controller health reconciliation repairs pending installation/revocation, checks target UID and policy drift, and never runs a custom CPU decision loop.

HPA continues while Hostlier is down. There is no independently enforced expiry guarantee. A manual action that cannot remove an HPA fails before changing replicas. Kubernetes owns missing metric behavior and stabilization history; no complete-sample freeze or durable per-recommendation transcript is claimed.

The completed evidence and remaining limits below distinguish real native runtime tests from simulated recovery coverage.

## Completed local real evidence

- Native CPU metrics chain PASS, `TestNativeHPACPU`, 724.98 seconds, 2026-10-29. k3s v1.34.11+k3s1, metrics-server v0.9.0. Real container CPU burn with 10m request/100m limit and target65% produced desired/ready1 ->2 at13:32:30/40Z ->3 at13:33:30/40Z. CPU load stopped13:33:40Z. Native HPA held3 through13:38:48Z, reduced to2 by13:38:58Z, then1 ready by13:43:57Z. Certificate-validated isolated HTTPS stayed200 throughout. Manual stop deleted HPA beforezero; lifecycle reconciliation did not wake it.
- Native revision handoff PASS, `TestNativeHPARevisionHandoff`, 15.50 seconds. An active policy survived lifecycle reconciliation, transferred only to the newly ready revision, and drained the source tozero. An explicit external replica field-manager patch caused durable pause and HPA deletion. A newly approved replacement resumed policy; a later approved zero revision stayedzero after reconstructing the runtime adapter, and `start` could not bypass zero approval.
- The original installed application and Redis namespaces were left intact. An authenticated read-only check of the preexisting Redis `fixture=persisted` marker passed after both isolated suites. Credentials remained inside the original workload and were not printed.
- Focused unit tests PASS: pending HPA installation restart recovery; durable pause before deletion and deletion failure retry; Deployment UID replacement; HPA max bound drift; externally deleted HPA; foreign owner non-deletion; metrics outage revocation; manualzero and foreign replica manager; retained/new ProductPolicy maximum intersection; manualscale/rollback revocation; unsafe schema guards.
- Full config/runtime/controller/CLI regression suites passed before unrelated concurrent skill migration removed the CLI's embedded `internal/skills/content/*/SKILL.md`. Latest runtime/controller/config suites still pass; CLI/E2E rebuild must rerun once that shared tree is restored. No touched production function exceeds complexity10 under AST control-flow/boolean counting.

Remaining research experiments: exhaustive synthetic timestamped metrics API scenarios, exhaustive crash points beyond the two critical real boundaries below, and detailed measurement of native HPA stabilization history after restart. No exhaustive coverage or durable native recommendation history is claimed.

## Final source follow-up

- Final-source `TestNativeHPARevisionHandoff` PASS13.41s; `TestNativeHPAActiveReconcile` PASS53.16s. The latter continuously reconciled lifecycle while genuine CPU scaling reached two ready pods, confirmed the pinned native `k3s` replica field manager was accepted, kept trusted HTTPS200 and manually removed the HPA before stopping capacity.
- Final-source `TestNativeHPACapacityKeepsServing` PASS38.88s. An owner-scoped CPU reservation made the HPA's second pod genuinely Unschedulable. The original serving pod retained trusted HTTPS200, desired count stayed capped at2 and node inventory stayed exactly the original single VM. Removing only the test reservation allowed the pending pod to become ready without changing image, resources, nodes or databases.
- Shared skill embedding was restored; all four config/runtime/controller/CLI regression suites PASS. New production and test functions satisfy complexity<=10. The original authenticated Redis marker passed again after the final suite.
- Cleanup inventory contained only default/system namespaces and the original application/Redis fixture namespaces. `limactl stop hostlier-k3s` began13:50:28Z and completed13:50:32Z; VM disks and installed fixtures are preserved.

## Real controller and VM restart acceptance (completed)

A fixture-only persistent TLS edge and an actual `controller.New` process run as two isolated systemd services, with dedicated protected runtime/controller directories and an owner-scoped application namespace. Original installed application, Redis, controller and Caddy configuration are untouched. The edge resumes its autosaved route and uses a persisted isolated CA after boot.

Real process failure injection has completed both critical HPA lifecycle boundaries:

- Abrupt exit85 after new-target readiness/HTTPS cutover and durable `phase=pending`, before replacement HPA apply. The journal retained the exact target, previous source and approved operation. Restarting the actual controller completed native HPA installation; immutable approved-operation replay then drained the source while preserving the installed HPA UID. PASS10.38s.
- Abrupt exit86 after durable `phase=paused`, before deleting the old HPA. No candidate capacity started. Restarting the actual controller completed deletion and kept the old policy revoked; only a fresh explicitly approved replacement restored delegation. PASS12.01s.

The restart exercise found and fixed a real startup hazard: transient Kubernetes API unavailability is now returned as an observation error rather than being mislabeled as proven ownership drift and permanently revoking a valid delegation. Proven UID/template/revision/ownership drift still revokes. A targeted regression proves the distinction.

Additional real acceptance results:

- With the actual isolated controller service stopped, genuine container CPU drove native HPA1 ->2 ready replicas. Restarting the actual controller retained the exact native HPA UID, approved bounds and trusted HTTPS200. PASS30.01s.
- Actual live-delegation VM shutdown completed14:12:03Z; boot was ready14:12:34Z. The enabled real controller and isolated TLS edge restarted automatically. The exact existing HPA UID remained installed, replica bounds remained1..2 and certificate-validated application HTTPS returned200. PASS0.11s after boot. The original authenticated Redis `fixture=persisted` marker also passed after reboot.
- Explicit manualzero serialized after stopping the fixture controller, durably removed HPA before creating zero capacity, and remainedzero after restarting the actual controller. PASS6.78s. A second actual VM shutdown completed14:13:42Z and boot was ready14:14:28Z. With the actual controller active after boot, HPA remained absent and desired replicas remained exactlyzero. PASS0.11s; the original authenticated Redis marker passed again.

The second reboot acceptance completed both critical persistence contracts. Fixture cleanup PASS14.83s removed only the two task-owned systemd services, their protected state/TLS files, the generated namespace and persistent test binary. Final namespace inventory preserved the original installed application, FAQ demo, Redis resource and system namespaces. Final VM shutdown began14:15:31Z and completed14:15:35Z. Full config/runtime/controller/CLI regressions pass with the startup observation fix; targeted restart harness compilation and cluster-startup regression also pass. Native HPA uses its upstream controller behavior; these results do not promise independently enforced expiry or durable recommendation/stabilization history.

The repository's actual quality scanner reports no missing function/type/control-flow comments or complexity violations in autoscaling-owned implementation or tests. Findings remaining elsewhere belong to concurrent workflow, typed-package and tunnel work.

Reproduction entry points are in `test/e2e/autoscaling_restart_test.go`. Build its Linux ARM64 E2E binary, install only the fixture copy at `/usr/local/lib/hostlier-hpa-restart-e2e.test`, and run the exact `TestNativeHPARestartCheckpoint` test as root inside the disposable VM with `HOSTLIER_AUTOSCALING_E2E=1`, `HOSTLIER_AUTOSCALING_RESTART_DIR=/var/lib/hostlier-hpa-restart-e2e`, and the named `HOSTLIER_AUTOSCALING_RESTART_PHASE`. The verified sequence was `setup`, expected exit85 `pending-crash`, `recover-pending`, expected exit86 `suspend-crash`, `recover-suspend`, `load-controller-down`, actual VM reboot, `live`, `zero`, actual VM reboot, `verify-zero`, then `cleanup` and removal of the fixture binary. The `cleanup` phase is also the owner-scoped recovery path for an interrupted test fixture. The two abrupt exits intentionally bypass deferred rollback to exercise persisted external lifecycle intent.

After comment-only quality cleanup, a repeat controller/CLI/E2E compile was temporarily blocked by concurrently added WebAuthn dependencies with missing `go.sum` entries. Config/runtime still passed; all four main suites passed with the final autoscaling behavior before that unrelated module edit. No autoscaling implementation changes or module-file edits were made in response.
