---
name: hostlier-resource
description: Provision, inspect, resize, back up or restore Hostlier private database resources.
---

# Hostlier resource

Resources are separate from applications. Inspect `hostlier resource list`, `resource status NAME` and `resource logs NAME` on the intended `--server`. Supported provisioning types are `postgres`, `redis` and `neo4j`, with optional packages installed on the controller host.

```sh
hostlier resource create postgres app-db --storage 5Gi
hostlier resource backup app-db --output ./new-backup-file
hostlier resource export BACKUP_ID --output ./another-new-file
hostlier resource restore postgres restored-db --storage 5Gi --backup BACKUP_ID
hostlier resource scale app-db --cpu 1 --memory 1Gi
```

Provisioning and restore require fresh hardware-signed exact-plan approval; resizing requires explicit maintenance approval. Let the user provide those approvals and do not bypass them with `--yes`. Neo4j Community backup requires a reviewed stop/dump/restart window. Export destinations must be new writable files; protect backup contents.

Lifecycle commands are `start`, `stop`, `restart` and `delete NAME`. Treat `delete --purge` as permanent owned-data removal, distinct from ordinary deletion. Respect active consumer and retained-reference checks. Use the returned resource binding metadata when configuring applications; do not expose database passwords in project files or chat.

The controller enforces human authority. When a request is pending, give its printed code to the human for `hostlier approve CODE --key FILE` from their trusted terminal; email enrollment and an agent API credential cannot sign it.

Database upgrades use `resource upgrade SOURCE --target NEW_NAME --to VERSION --destination ID --watch`. Supported first paths are PostgreSQL 16→17, Redis 7.4→8.2 and Neo4j 5.26→2025.01 on Docker. Detach source dependencies first. This creates an independent database, fences both identities, stops the source, compares native data and verifies an encrypted S3 recovery copy. It does not replace an image in place.

Inspect `resource upgrade-status ID --watch`. A verified `awaiting-cutover` job may proceed to separately approved `resource cutover ID`; bind applications to the new target afterward. `resource abort-upgrade ID` removes the temporary target before reopening the source. Never reopen the old source after cutover: subsequent target writes cannot be undone by switching images.

After an uncertain cutover attempt, inspect the retained phase. A fresh `resource cutover ID` approval can reconcile it only if the original verification still matches. Abort is blocked once cutover starts because the target might have accepted writes. Source storage stays stopped and reserved after successful cutover. Kubernetes major-version upgrades are rejected before source mutation; supported database backups and verified imports remain available.

## Application dependency schema

Applications can provision a database in the same approved deployment plan:

```yaml
dependencies:
  database:
    type: postgres
    source: provision
    storage: 2Gi
bindings:
  DATABASE_URL: database.url
```

To attach an existing resource, use `source: existing` and `instance: RESOURCE_NAME` instead of `storage`. Use the destination environment name required by the application, for example `FUSED_DATABASE_URL` rather than `DATABASE_URL`. Hostlier injects credentials transiently; do not copy them into YAML or print them. `resource create` has no database version-selection flag. Inspect its plan for the package's pinned image/version before approval. If the required major is unavailable, use a documented application-owned database sidecar or install an appropriate supported package; see `hostlier skills show deploy` for the complete sidecar schema.
