---
name: hostlier-backup
description: Configure approved Hostlier S3 backup policies, inspect recovery jobs, or import an encrypted database snapshot.
---

# Hostlier backups

Use `hostlier backup destinations` to inspect operator-configured storage. Keys stay on the controller. Propose `backup policy NAME --resource DB --destination ID --every 24h --retain 7`; each run creates and purges a temporary restore-test database. Neo4j requires offline maintenance and detached dependencies. Independent hardware approval authorizes the precise destination, schedule and retention.

Run `backup run NAME --watch`, or inspect `backup job ID --watch`. A failed/uncertain job is not a verified recovery point. Inspect its retained phase before a separately approved `resource abort-upgrade ID` cleans up a failed verification target. Do not duplicate ambiguous work.

Discover portable objects with `backup objects DESTINATION`. `backup import OBJECT_ID --destination ID` authenticates encrypted metadata before producing a local backup ID; use `resource restore ENGINE NEW_NAME --storage SIZE --backup ID` to restore and verify it. Controller credentials alone do not recover S3 data: the independent encryption key must be preserved.

Use `--on HOST` consistently to select an enrolled host. Its S3 credentials and recovery key remain on that host; the central controller authorizes each scheduled run and stops scheduling when the approving key is revoked. A disconnected central controller cannot dispatch new remote runs.

Policy names are immutable: disable an old policy and approve a new name when changing its scope. Scheduling is bounded to intervals of 1 hour–31 days, with 1–100 verified copies. Native snapshots are limited to 64 MiB. PostgreSQL covers the managed database, Neo4j covers `neo4j` (not system users), and Redis verification covers databases 0–15 with string/list/set/sorted-set/hash values; streams and module types fail verification. Do not describe a native backup as a whole-server or controller-state backup.
