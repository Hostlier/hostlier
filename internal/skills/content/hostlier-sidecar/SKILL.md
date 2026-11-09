---
name: hostlier-sidecar
description: Attach or detach a Hostlier agent or application sidecar within an existing deployment group.
---

# Hostlier sidecar

Inspect `hostlier status APP` and its complete group configuration. Attach a single component described by a version 2 member file:

```sh
hostlier sidecar attach APP --name assistant --file assistant.yaml --server production
hostlier logs APP --component assistant --server production
hostlier sidecar detach APP --name assistant --server production
```

A loopback service declares `endpoint.exposure: loopback`, its port, and an explicit `health.exec` supported by the actual image. It shares the group's loopback network; its ports are not published. Members receive only their own declared environment, secrets and volumes. Do not assume inherited credentials or shell/curl availability.

Attach rejects an existing member name; replacing a member uses the complete desired project through update. Detach rejects a member still referenced by bindings. Nested sidecar groups and port collisions are invalid. Both actions plan and redeploy the whole group, may restart the primary, and require exact-plan authority. Let the user supply approval. Group rollback restores the whole revision, not just one member.

A matching window can authorize stateless redeployment without a fresh prompt. Otherwise hand the printed request code to the human for `hostlier approve CODE --key FILE` in their own terminal; do not simulate the signature. Data-bearing changes remain outside stateless windows.
