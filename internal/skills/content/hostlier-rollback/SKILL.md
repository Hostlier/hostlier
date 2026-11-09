---
name: hostlier-rollback
description: Roll back a Hostlier application to the previous or an explicitly selected retained revision.
---

# Hostlier rollback

Inspect `hostlier status APP --server production` for the current application and retained revision evidence. Omit the revision to select the revision immediately before the active deployment, or use a retained revision ID from that evidence or the user:

```sh
hostlier rollback APP --server production
hostlier rollback APP --revision REVISION --server production
```

The default fails if no previous revision is available and pins the selected ID before approval. Every successful rollback creates a new revision; another default rollback returns to the deployment that preceded it.

Rollback reads the existing server specification and plans the retained revision; it does not need a local project file. Review the exact proposed route, immutable artifacts, bindings and volume implications. For sidecar groups, rollback restores the whole retained group revision. It does not roll back application or database data.

The CLI requires controller-enforced exact-plan approval or a matching signed deployment window and rejects `--yes`. Let the user supply approval. Track the accepted operation to a terminal result and inspect status and logs. Do not repeatedly retry a failed rollback without understanding the reported cause.

A matching window can authorize stateless redeployment without a fresh prompt. Otherwise hand the printed request code to the human for `hostlier approve CODE --key FILE` in their own terminal; do not simulate the signature. Data-bearing changes remain outside stateless windows.
