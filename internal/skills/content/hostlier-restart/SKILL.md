---
name: hostlier-restart
description: Restart an existing Hostlier application or deployment group and verify readiness.
---

# Hostlier restart

Inspect `hostlier status APP --server production` to identify the intended application, then run `hostlier restart APP --server production` within the user's requested scope.

Restarting interrupts the existing application; it does not apply edits in a local project file. For sidecar groups, account for group lifecycle ordering and verify the primary and required members recover.

The CLI follows accepted operations automatically. `--detach` returns before completion; use `hostlier operation ID --watch` to reattach. An interrupted observer leaves server work running. Report the terminal operation result and observed application state. Database resources use `hostlier resource restart NAME` instead.
