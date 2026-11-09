---
name: hostlier-hosts
description: Enroll, inspect or revoke host agents managed by a central Hostlier controller.
---

# Hostlier hosts

`hostlier hosts identity` shows the central controller certificate fingerprint. `hostlier hosts list` shows enrolled hosts. Verify each host identity through a trusted channel before `hosts enroll NAME --url https://HOST:PORT --fingerprint SHA256`. Enrollment and `hosts revoke NAME` require human approval. Use `--on NAME` to target a workload command. Certificate pinning binds identity, not only address. On disconnect, inspect the existing operation; do not deploy onto another host as an automatic retry.
