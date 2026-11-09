---
name: hostlier-authorize
description: Prepare a bounded Hostlier deployment window for a device, host, applications and secret references.
---

# Hostlier authorize

Use `hostlier authorize --device ID --on local|HOST --app APP[,APP] --scope deploy --for 2h --max-uses 20`. Include `--secrets NAME[,NAME]` only for existing secret references needed by those apps. A human must independently approve through the controller browser page with a passkey; alternatively they may add `--key FILE` from their own trusted terminal. Maximum eight hours and 100 uses. This grants arbitrary code deployment for named stateless apps, including access to listed secrets. It excludes persistent volumes, stateful source apps, dependencies, workflows and administrator operations. Do not broaden scope to bypass rejection.

For agents creating several test apps, the owner can grant a sandbox window instead: `hostlier authorize --device ID --on local --app-prefix try- --max-apps 5 [--route-domain try.apps.example.com] [--keep-apps] --scope deploy --for 8h --max-uses 100`. The agent may then deploy new `try-*` apps up to the cap, update the ones its sandbox created, and stop, start, restart or delete them (never purge). Apps the sandbox created are deleted when the window ends unless `--keep-apps` was set. Name test apps with the granted prefix; do not try to adopt existing apps outside it.
