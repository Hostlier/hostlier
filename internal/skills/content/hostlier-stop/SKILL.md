---
name: hostlier-stop
description: Stop an existing Hostlier application while respecting active consumer references.
---

# Hostlier stop

Inspect `hostlier status APP --server production` to identify the intended application, then run `hostlier stop APP --server production` within the user's requested scope.

Stopping affects service availability. Active private-service consumer bindings may prevent stopping a provider. Respect that protection and inspect consumers rather than changing controller state directly.

The CLI follows accepted operations automatically. `--detach` returns before completion; use `hostlier operation ID --watch` to reattach. An interrupted observer leaves server work running. Report the terminal operation result and observed application state. Database resources use `hostlier resource stop NAME` instead.
