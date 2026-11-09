---
name: hostlier-controller
description: Inspect Hostlier controller events or perform server-local controller backup and recovery.
---

# Hostlier controller

Use `hostlier controller status` and `hostlier controller logs --operation ID` for authenticated diagnostics. `--follow` follows events; the protected `--socket /run/hostlier/controller.sock` supports server-local administration.

Backup and restore operate locally, independent of a remote connection:

```sh
hostlier-controller backup --output /safe/new-backup-file
hostlier-controller restore --input /safe/backup-file --state-dir /var/lib/hostlier-restored --source-stopped
```

Inspect the actual installation and recovery requirements before running them. Recovery uses the writer lock; never bypass it. `--source-stopped` is an assertion that the source controller is stopped, not a request to stop it. Keep backup material private and use a new intended destination. Restoring controller state is not a substitute for application or resource data backups.

`hostlier-controller run` starts the local daemon and requires valid installation configuration and runtime dependencies; normal installed service management belongs to systemd.
