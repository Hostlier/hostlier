---
name: hostlier-authorizations
description: Inspect or revoke Hostlier deployment authorization windows.
---

# Hostlier authorizations

Use `hostlier authorizations list` to inspect devices, hosts, apps, secret references, expiry and usage. `hostlier authorizations revoke ID` requires an independent approval. A matching window never approves changing or extending itself. Device/key/host revocation and expiry also remove authority. Only the controller determines whether a request falls within the retained window.
