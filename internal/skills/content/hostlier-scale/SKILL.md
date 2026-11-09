---
name: hostlier-scale
description: Change a Hostlier application CPU, memory or replica allocation on existing server capacity.
---

# Hostlier scale

Read `hostlier status APP` and `hostlier usage APP` on the intended server before choosing requested allocation values:

```sh
hostlier scale APP --cpu 1 --memory 512Mi --replicas 1 --server production
```

Supply at least one allocation flag. Omitted values preserve the current server settings, and the command does not edit local project files. It changes application allocation on existing capacity, not server size or persistent storage capacity. Database allocation uses `hostlier resource scale`.

Docker supports zero or one replica. k3s supports multiple stateless replicas; workloads with local persistent volumes remain single-replica. Sidecars share the parent placement and replica count. The CLI requires controller-enforced exact-plan approval or a matching signed deployment window and follows the operation automatically. Let the user supply approval, then verify observed allocation and application readiness.

A matching window can authorize stateless redeployment without a fresh prompt. Otherwise hand the printed request code to the human for `hostlier approve CODE --key FILE` in their own terminal; do not simulate the signature. Data-bearing changes remain outside stateless windows.

For automatic idle scaling, set `sleepAfter: 10m` in the service’s `.hostlier` file and deploy the policy. Hostlier stops the single stateless HTTP service after the interval and serves the first request after waking it. The service needs a public root route. Inspect `runtimeStatus.idleSleeping` with `hostlier status APP --json`. A manual scale revokes the policy.
