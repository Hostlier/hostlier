---
name: hostlier-usage
description: Inspect observed Hostlier application resource usage and configured allocation limits.
---

# Hostlier usage

Run `hostlier usage APP --server production`. It requires exactly one application name and is an authenticated read. Compare returned measurements with configured limits and workload state; distinguish unavailable measurements from actual zero usage.

Use application status and operation progress to determine whether a recent change has converged. If a resize is requested, `hostlier scale APP --cpu CORES --memory SIZE --replicas N` changes allocation after plan approval. Usage inspection alone does not authorize a resize or provision more server capacity. Report measurement limitations when the runtime does not supply a metric.
