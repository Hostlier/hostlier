---
name: hostlier-hosts
description: Enroll, inspect or revoke host agents managed by a central Hostlier controller.
---

# Hostlier hosts

`hostlier hosts identity` shows the central controller certificate fingerprint. `hostlier hosts list` shows enrolled hosts. Verify each host identity through a trusted channel before `hosts enroll NAME --fingerprint SHA256`. The box must have the master fingerprint and `--master-url https://MASTER:7464`, or use `sudo hostlier-controller connect https://MASTER --name NAME` on the member and `sudo hostlier-controller approve CODE` on the master. These two commands exchange fingerprints automatically and require no browser or explicit socket path. Controllers initiate persistent gRPC connections to the master; the optional enrollment URL is display metadata. Enrollment and `hosts revoke NAME` require human approval. Use `--on NAME` to target a workload command. Certificate pinning binds identity, not only address. On disconnect, inspect the existing operation; do not deploy onto another host as an automatic retry.
