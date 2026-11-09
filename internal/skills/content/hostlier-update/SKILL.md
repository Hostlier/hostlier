---
name: hostlier-update
description: Update an existing Hostlier application from its complete desired project configuration.
---

# Hostlier update

Read the current application with `hostlier status APP --server production` and inspect its local project file. `hostlier update --file .hostlier --server production` uses the same file-driven planning and approval flow as deploy; it is not `update APP` and does not infer a new image from a tag.

Preserve the complete desired configuration, including sidecars, secret references, volumes and bindings. Validate and plan the edited file before updating. A group update can restart its primary process. Writable volume changes may require a maintenance window; an image rollback does not restore application data.

The CLI requires controller-enforced exact-plan approval or a matching signed deployment window, and `--yes` cannot bypass it. Let the user provide required approval. Follow the returned operation to completion, then verify application status and logs. A detached or interrupted watch does not cancel accepted server work.

A matching window can authorize stateless redeployment without a fresh prompt. Otherwise hand the printed request code to the human for `hostlier approve CODE --key FILE` in their own terminal; do not simulate the signature. Data-bearing changes remain outside stateless windows.
