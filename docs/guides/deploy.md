# Deploy and update applications

[All guides](../../GUIDE.md)

For a first deployment from the Hostlier source directory, the included nginx example needs only a route under your configured domain:

```sh
hostlier deploy --file examples/hello.hostlier --route hello.apps.example.com
```

Both top-level `image` and version 2 `source.image` discover the receiving
controller's architecture, try downloading there first, and automatically fall
back to your local Docker login and upload if its registry access fails.
See [private images](private-images.md) for registry setup.

Create `.hostlier` in the current project directory:

```yaml
version: 1
name: analytics
image: ghcr.io/acme/analytics:1.0
port: 3000
route: analytics.apps.example.com
health:
  path: /health
env:
  NODE_ENV: production
secrets:
  API_KEY: $ANALYTICS_API_KEY
volumes:
  uploads:
    mount: /app/uploads
    size: 5Gi
```

Set `ANALYTICS_API_KEY` in the local shell, then deploy. Hostlier synchronizes it automatically; a separate `secret set` command is optional:

```sh
hostlier validate
hostlier plan
hostlier deploy
hostlier status analytics
hostlier logs analytics --follow
```

Secret declarations use flat strings:

```yaml
secrets:
  INLINE_TOKEN: "literal-value"
  API_KEY: $ANALYTICS_API_KEY
  EXISTING_TOKEN: secret://analytics-api-key
  LITERAL_DOLLAR: $$starts-with-a-dollar
```

A plain string is the value itself. `$VARIABLE` reads one exported environment variable from the machine running the CLI; it does not read the VPS environment, load `.env` files, interpolate strings or run shell commands. `$$` escapes a leading dollar. `secret://NAME` references an existing controller secret without uploading a value. Quote numeric values to keep them strings.

`validate` checks local sources offline. `plan`, `deploy`, and sidecar attachment synchronize declared values through the normal authenticated secret API before planning. Both `plan` and `deploy` can therefore create or update secret storage, but only deployment changes a running application. All local values and the complete project must validate before secret uploads begin. Missing, empty and oversized values fail without printing their contents. Uploads require secret write permission; restricted use-only or creation-only credentials cannot overwrite stored values. An all-access connection can synchronize them.

Hostlier assigns stable names per application, component and environment key. Values are encrypted on the controller, and plans contain references only. Unchanged values reuse their revision on an updated controller; changed values create a revision for the next deployment. Existing deployments and rollback history keep their pinned revisions. Failed later uploads or deployments leave earlier synchronized secrets stored for retry; uploads are not a multi-secret transaction. Literal values remain plaintext in the authored file and may enter source control or a Docker build context; `$VARIABLE` keeps the value out of that file.

**Migration:** application `secrets` entries that previously used bare stored names must become `secret://NAME`. Bare strings now mean literal values. Controller API projects and workflow/package secret references retain their existing reference-only contract.

Deployments and protected changes show a concrete plan and require approval. A matching [bounded deployment window](../email-device-auth.md#authorize-a-bounded-deployment-window) can authorize repeated stateless deployments without another prompt. There is no deployment `--yes` bypass. `--detach` returns after durable acceptance; the controller continues the job. Apply retries return the original operation. Interrupted jobs are marked for inspection and renewed approval rather than repeating uncertain mutations automatically.

`--file FILE` selects a project file, `--server NAME` selects a profile, and `--route ADDRESS` replaces the complete route for one plan/deployment. Routes support a hostname and optional path, such as `example.com/analytics`. Prefixes are preserved unless `proxy.stripPrefix: true` is set. Domain membership, reserved controller addresses, and overlapping routes are enforced by the controller.

Images resolve to immutable digests during preparation and planning. Updates with persistent volumes use a stop/start strategy to prevent simultaneous writers. Ordinary Docker volume sizes are requested capacity, not enforced quotas. Stop keeps the workload's containers, images, and data while its capacity remains stopped across restarts. Delete removes all containers owned by that application, including stopped older revisions, and releases image cache entries once no active app or plan needs them. Normal deletion retains data and hides the app from the default `hostlier list`; use `hostlier list --all` to inspect retained records. Explicit purge removes owned data.

Roll back to the revision immediately before the active deployment with `hostlier rollback NAME`, or choose a retained revision with `hostlier rollback NAME --revision ID`. Use `hostlier status NAME --json` to inspect revision history. The default requires a previous revision and pins its ID before plan review, including if a stale plan needs renewed approval. Each successful rollback creates a new revision, so another default rollback returns to the deployment that preceded it. Rollback restores application configuration and pinned secrets; it does not undo database or persistent-volume data changes.

Other commands include `list`, `update`, `start NAME`, `stop NAME`, `restart NAME`, `delete NAME`, `domains`, `clients`, and `controller logs`. Use `--json` for data output. Server-local administration uses the protected Unix socket.

For multiple HTTP/gRPC listeners, streaming transports and versioned config files, see [Named endpoints and managed files](endpoints.md).

Next: [Scale and monitor](scale-and-monitor.md) or [add a database](databases.md).
