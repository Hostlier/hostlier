---
name: hostlier-swarm
description: Group K3s applications and manage permission-scoped membership using the CLI.
---

# Hostlier swarm

Use `hostlier swarm list`, `create NAME`, `show NAME`, `members NAME`, `move APP --from SOURCE --to DESTINATION`, and `delete NAME`. Select a saved login with `--server PROFILE` and an enrolled controller with `--on HOST`. Mutations use terminal confirmation and existing signed approval receipts.

Named swarms require K3s. Existing apps belong to permanent `default`; app names remain unique per server. Set top-level `swarm: NAME` in `.hostlier` or use `deploy --swarm NAME`. Reject conflicting selectors and create groups explicitly. Sidecars inherit their app's group. Moving changes metadata only; update the manifest before redeploying. Namespaces, routes, storage and revisions retain their identity. Grouping does not join Kubernetes clusters or change networking.

Use `app list --swarm NAME` for active records and `--all` for retained deleted records. `swarm members NAME` includes retained deleted records. Groups must be empty before deletion; active or interrupted app operations block moves. Purge remains a separate permission and action.

An administrator can grant `swarms.view` for scoped discovery or `swarms.manage` for grouping. Use `access authorize --device ID --on local|HOST --scope actions --swarm NAME --actions apps.deploy,apps.rollback --for 2h --max-uses 20` for workload permissions; add `--app APP` to narrow them. Omitting `--app` with an explicit swarm covers the group. Creation and cross-group moves require an unbound `swarms.manage` grant or all access. Group management cannot issue grants or mutate workloads. Existing stateless and secret restrictions still apply. Do not broaden authority to bypass rejection.

Old grants are limited to default. Deleted-and-recreated groups have new identities. Named-group state fences older controller binaries; use a compatible pre-upgrade snapshot and the documented recovery procedure for rollback. On-demand sessions, microVM execution and MCP service commands are not provided by this release.
