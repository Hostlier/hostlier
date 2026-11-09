---
name: hostlier-domains
description: Inspect or change the Hostlier server domain allowlist used by deployment routes.
---

# Hostlier domains

Use `hostlier domains list --server production` to inspect the allowlist. Add the authorized domain with `hostlier domains add example.com`; remove it with `hostlier domains remove example.com` when requested. `hostlier domain` is an alias.

The allowlist controls which deployment addresses are permitted. The actual route belongs in the application project or a deployment's `--route` flag. Adding a domain does not configure public DNS or a provider firewall. Check those separately when public access fails.

Removal is rejected while active applications or accepted deployments still use the domain. If editing `/etc/hostlier/config.yaml` directly is necessary, the controller must be restarted to reload it; prefer the CLI for ordinary changes.
