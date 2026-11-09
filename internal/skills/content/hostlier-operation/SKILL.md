---
name: hostlier-operation
description: Inspect or reattach to a durable Hostlier operation after deployment or lifecycle changes.
---

# Hostlier operation

Use a real operation ID returned by a Hostlier command:

```sh
hostlier operation ID --server production
hostlier operation ID --watch --server production
```

The first command reads status; `--watch` follows progress and reconnects automatically. Ctrl-C stops the local observer and leaves accepted server work running. `--detach` on a mutating command likewise returns before completion.

Distinguish acceptance, active progress and terminal success or failure. Do not submit another deployment just because a watch was interrupted; reattach to the existing operation first. For failure analysis, correlate its ID with `hostlier controller logs --operation ID` and the application's status and logs. Report the operation ID and observed outcome without treating missing observations as success.
