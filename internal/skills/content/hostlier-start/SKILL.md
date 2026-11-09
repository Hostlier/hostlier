---
name: hostlier-start
description: Start an existing stopped Hostlier application and observe its operation.
---

# Hostlier start

Inspect `hostlier status APP --server production` to identify the intended application, then run `hostlier start APP --server production` within the user's requested scope.

Starting uses the retained deployment configuration; it does not build a new image or update the local project. Verify the final observed readiness after the operation succeeds.

The CLI follows accepted operations automatically. `--detach` returns before completion; use `hostlier operation ID --watch` to reattach. An interrupted observer leaves server work running. Report the terminal operation result and observed application state. Database resources use `hostlier resource start NAME` instead.
