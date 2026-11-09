---
name: hostlier-disconnect
description: Remove a local Hostlier server connection profile without revoking its server-side credential.
---

# Hostlier disconnect

Inspect `hostlier server list`, then run `hostlier disconnect NAME` for the profile the user wants removed. The command changes local connection state under `HOSTLIER_HOME` or `~/.hostlier`.

Disconnecting does not revoke the enrolled client credential on the server, stop applications or uninstall the controller. If revocation is the user's intent, use an authorized remaining administration connection to inspect `hostlier clients list` and revoke the identified client. Do not disconnect an unrelated profile or delete the entire Hostlier home directory.
