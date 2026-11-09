# Scale and monitor applications

[All guides](../../GUIDE.md)

## Monitor

Run `hostlier ui --server production` for a loopback dashboard with a simple application overview. Open a deployed app for its own Overview, Metrics, Logs, Activity, Resources and Settings. Controller settings remain separate; management changes use copyable CLI commands and the existing approval flow. The printed URL contains a local browser session capability; controller credentials stay in the CLI. See [the observatory architecture and limits](../observatory.md).

The CLI follows progress and reconnects automatically. After exiting or using `--detach`, run `hostlier operation ID --watch` to reattach. See [streaming details](../../STREAMING.md).

## Sleep an idle HTTP service

Set `sleepAfter` in the service’s `.hostlier` file and deploy it. This example permits Hostlier to run one replica on demand and stop it after ten minutes without active requests.

```yaml
version: 1
name: analytics
image: nginx:1.28-alpine
port: 80
route: analytics.example.com
sleepAfter: 10m
```

The timer runs from the last completed request. Open streams and WebSockets keep the service awake. The first request after sleep waits for readiness and is then forwarded with its original method, path, and body. Concurrent requests share the same wake-up. A cold start has a 90-second runtime transition budget. If startup fails, the waiting request receives HTTP 503 and a later request can retry. Use `hostlier status analytics --json` to inspect `runtimeStatus.idleSleeping`.

Idle scaling supports Docker and k3s services with one stateless replica and one public HTTP endpoint at the root of its hostname. Set a duration from `1s` to `720h`. Choose an app whose work is driven by requests on that route, since private traffic, background jobs, and scheduled work do not count as activity. The controller and VPS keep running while the workload sleeps. Manual scaling or rollback revokes idle scaling. Deploy the config again to restore it.

The [fresh k3s VM test](../../test/e2e/VM_TESTS.md#fleet-handoff-on-fresh-vms) verifies the first POST is delivered once both before and after a controller restart at zero replicas.

## Scale

Hostlier automatically installs its minimal OpenTelemetry collector once per Docker host or Linux k3s node. Release bundles include the image assets, and the controller manages credentials, authenticated image pulls, and repair. See [collector setup](../telemetry-collectors.md) and the [minimal build](../../collector/README.md).

```sh
hostlier scale analytics --cpu 2 --memory 2Gi
hostlier scale analytics --replicas 3    # k3s, stateless services
hostlier scale analytics --replicas 0
hostlier usage analytics
```

Omitted flags preserve current server settings. Every scale needs interactive approval and follows progress automatically; local files stay unchanged. Docker supports zero or one instance. k3s supports multiple stateless replicas; local volume workloads remain single-replica. These commands change application allocation on existing capacity, not server size or storage capacity.

Stateless replacements become ready before traffic switches and the previous workload stops. Volume-backed updates use a stop/start maintenance window. Scale-to-zero reserves the route with HTTP 503 and retains data; it stays stopped across reboot until another approved scale. CPU/memory values apply per replica. See [usage measurements](../runtime-usage.md) for observed consumption and unavailable metrics.
