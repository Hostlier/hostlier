---
name: hostlier-delete
description: Delete a Hostlier application with explicit separation between removal and permanent data purge.
---

# Hostlier delete

Inspect `hostlier status APP --server production` to identify the intended application, then run `hostlier delete APP --server production` within the user's requested scope.

Ordinary deletion and `delete APP --purge` have different data consequences. Purge permanently removes owned application data and requires separate hardware-signed approval. Use it only when the user requested that data removal; do not pipe an invented approval. Respect active bindings and retained-reference protections.

The CLI follows accepted operations automatically. `--detach` returns before completion; use `hostlier operation ID --watch` to reattach. An interrupted observer leaves server work running. Report the terminal operation result and observed application state. Database resources use `hostlier resource delete NAME` instead.

The controller enforces human authority. When a request is pending, give its printed code to the human for `hostlier approve CODE --key FILE` from their trusted terminal; email enrollment and an agent API credential cannot sign it.
