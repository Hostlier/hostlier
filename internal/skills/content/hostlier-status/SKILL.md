---
name: hostlier-status
description: Inspect Hostlier controller health or a deployed application and its retained state.
---

# Hostlier status

Use `hostlier status --server production` for controller-wide state or `hostlier status APP --server production` for one application. These are authenticated reads and do not start, stop or redeploy anything.

Distinguish desired configuration, observed runtime state and pending operations. If an operation is still running, inspect its returned ID with `hostlier operation ID` or follow it with `--watch`. Correlate failure evidence with `hostlier logs APP`, component logs for sidecars, or `hostlier doctor`. Use returned revision IDs for rollback planning rather than inventing them. Report uncertainty when the controller cannot observe the runtime.
