# Group applications with swarms

A swarm is a logical application group on one controller. Named swarms require K3s. Grouping keeps application namespaces, routes, volumes, backups, revisions and runtime identities intact. It does not join controllers into a Kubernetes cluster or change network policy. App names remain unique per server in this release.

Existing applications are assigned to the permanent `default` swarm when the controller starts. Existing deployment and sandbox grants remain limited to `default`; group membership alone grants no access.

## CLI workflow

Use your saved login. Every command supports `--server PROFILE` and `--on HOST` for an enrolled controller reached through its outbound gRPC session.

```sh
hostlier swarm create research --server primary --on worker-1
hostlier swarm list --server primary --on worker-1
hostlier swarm show research --server primary --on worker-1
hostlier deploy --file .hostlier --swarm research --server primary --on worker-1
hostlier app list --swarm research --server primary --on worker-1
hostlier swarm members research --server primary --on worker-1
```

Alternatively add `swarm: research` to the top level of `.hostlier`. The group must already exist. Conflicting file and CLI selections are rejected. Sidecars inherit their application's group. Memory-controller exports do not support named swarms.

Move an existing app explicitly:

```sh
hostlier swarm move api --from default --to research --server primary --on worker-1
```

This changes membership only and invalidates pending plans and approvals. Queued, running or interrupted app operations must be resolved first. Update the application's `.hostlier` selection before its next deployment. Existing commands such as `hostlier app stop api` continue to use the server-wide app name; the controller checks its current group.

```sh
hostlier swarm delete research --server primary --on worker-1
```

Deletion requires an empty group, including retained deleted applications. `swarm members` includes these retained records; `app list --swarm research --all` does too. Move or purge retained apps before deleting their group. `default` cannot be deleted. Recreating a name creates a new identity and does not restore old grants.

Mutations show a terminal confirmation and use retained, signed approval receipts. An all-access login or a matching action grant completes them without a browser.

## Permissions

An owner or all-access administrator grants permissions to an existing signed-in device:

```sh
# Discover applications within one group.
hostlier access authorize --server primary --device DEVICE_ID --on worker-1 \
  --scope actions --swarm research --actions swarms.view --for 2h --max-uses 20

# Deploy, roll back and stop apps in this group.
hostlier access authorize --server primary --device DEVICE_ID --on worker-1 \
  --scope actions --swarm research --actions apps.deploy,apps.rollback,apps.stop \
  --for 2h --max-uses 20

# Administer all groups on this controller, including creation and moves.
hostlier access authorize --server primary --device DEVICE_ID --on worker-1 \
  --scope actions --actions swarms.manage --for 2h --max-uses 20
```

Use `--on local` to target the master controller itself. Adding `--app api` narrows a group workload or viewing grant to that app. Group administration grants do not accept an app allowlist. Omitting `--app` with an explicit `--swarm` grants the listed app actions throughout that group. Without `--swarm`, app action grants still require exact app names and apply only in `default`.

`swarms.view` permits group discovery and application reads, including logs, within its scope. `swarms.manage` permits grouping changes and discovery; it confers neither workload mutation permissions nor authority to issue grants. A group-bound manager can delete its empty group. Creating groups and moving between groups require an unbound `swarms.manage` grant or all access. One grant must cover the complete action.

Existing action permissions remain separate: `apps.scale`, `apps.start`, `apps.restart`, `apps.remove`, `apps.purge`, `sidecars.remove`, `servers.add` and `servers.remove`. Purge requires both remove and purge permissions; removing a sidecar also needs the deployment or rollback permission; deploying or scaling to zero requires stop permission. Secret use and stateless-deployment restrictions remain in effect. See [controller access](controller-access.md#action-permissions).

Grants bind the immutable group ID and selected controller identity. Expiry and revocation still apply. Moving an app removes its former group's access to its status, logs, plans, operations and approval receipts, including watches.

## Upgrade and recovery

Backfill is saved before controller workers and listeners start. It preserves existing project specifications and plan fingerprints. Creating the first named swarm writes controller state version 3, which older controllers reject. Deleting all named groups does not remove this downgrade fence.

Metadata exports retain groups, membership and group-bound grants. Keep the pre-upgrade backup if binary rollback may be necessary: an older binary requires its compatible snapshot and the normal recovery/reconciliation process. Do not edit state versions to force a downgrade.

This release provides grouping, CLI commands and permissions. On-demand sessions, MCP service commands, duplicate app names across groups and microVM execution remain future work in the [implementation plan](../implementation-swarms.md).

See the [live K3s validation results](../swarm-validation.md) for tested remote flows and their limits.
