---
name: hostlier-clients
description: Inspect enrolled Hostlier clients and revoke an explicitly identified client credential.
---

# Hostlier clients

Use `hostlier clients list --server production` or the server-local `--socket /run/hostlier/controller.sock` to inspect enrolled client identities. The alias `hostlier client` is also accepted.

When revocation is requested, match the intended identity to its returned ID and run `hostlier clients revoke ID` against that same controller. Revocation removes the client's authority; it does not delete its local profile. Revoking the connection in use can prevent further remote verification, so retain another authorized administration path when needed. Never infer that every enrolled client should be revoked from a request to disconnect one local profile.
