# Run scheduled jobs

[All guides](../../GUIDE.md)

Use a standalone `kind: cron` project to run a container on a schedule on a Hostlier K3s host. Save this example as `report.hostlier`.

```yaml
version: 2
kind: cron
name: report
schedule: "0 7 * * 1-5"
timeZone: Europe/London
source:
  image: docker.io/library/busybox:1.37
command: [sh, -c]
args: ["echo report completed"]
resources:
  cpu: "0.25"
  memory: 64Mi
```

The example runs at 07:00 London time on weekdays. Use a five-field cron expression or a Kubernetes calendar descriptor such as `@daily`. The time zone defaults to `Etc/UTC`. Swap in your image and its job command. You can use `env`, `secrets`, read-only `configs`, resource limits and requests. Each job runs to completion without a public route, persistent volume, or always-on service. Set `swarm` to an existing swarm name to register there. Omit it to use the default swarm.

```sh
hostlier plan --file report.hostlier
hostlier deploy --file report.hostlier
hostlier cron status report
hostlier cron pause report
hostlier cron run-now report
hostlier cron history report
hostlier cron logs report RUN_ID --tail 100
hostlier cron resume report
```

Plan previews the schedule and pinned image. Deploy uses the regular approval flow and enables a new schedule. Deploying the file again updates the schedule and preserves its paused state. A deployment or lifecycle change requires an all-access connection and follows the controller's approval policy. Use `--server PROFILE` to select a connection and `--on HOST` to select an enrolled K3s worker.

The first cron deployment upgrades controller state to version 4. That state requires a cron-capable Hostlier controller.

Pause stops automatic starts without interrupting jobs already running. Run-now starts the deployed revision even while paused and returns a run ID once K3s accepts the Job. Use history to check whether it succeeded and logs to read its output. K3s skips overlapping scheduled runs and may catch up an eligible missed start within five minutes. Manual runs can overlap other runs.

K3s keeps up to three successful and three failed runs per schedule and expires finished runs after 24 hours. Logs are available while the run's pod is retained. Use `hostlier cron delete report` to remove the schedule and its namespace, including active runs, history and mounted configuration. A Docker-backed Hostlier server needs a K3s worker or a runtime migration before it can register cron jobs.

## Schedule jobs alongside a service

Add a `cron` map to an existing service's `.hostlier` file. Each key names a job, and each job specifies a schedule and an explicit command.

```yaml
version: 1
name: analytics
image: ghcr.io/acme/analytics:1.0
port: 3000
route: analytics.apps.example.com
cron:
  daily-report:
    schedule: "0 7 * * 1-5"
    timeZone: Europe/London
    command: ["/app/analytics", "report"]
    args: ["--daily"]
  cleanup:
    schedule: "@weekly"
    command: ["/app/analytics", "cleanup"]
```

Run the same `hostlier plan` and `hostlier deploy` commands you use for the service. The plan includes the jobs and any schedules it will remove. Each job uses the service revision's pinned image, `env`, `secrets`, managed files, resource limits, and container security settings. Jobs run in their own pods, without service volumes, sidecars, or a listener. The service and its jobs share swarm membership. Both version 1 and version 2 service files support this property on K3s. Cron changes use the same all-access approval policy as standalone jobs.

```sh
hostlier cron status analytics/daily-report
hostlier cron pause analytics/daily-report
hostlier cron run-now analytics/daily-report
hostlier cron history analytics/daily-report
hostlier cron logs analytics/daily-report RUN_ID
hostlier cron resume analytics/daily-report
```

Stopping the service leaves its schedules active. Pause a job to suspend automatic runs. Redeploying a retained job updates its template and keeps its pause state and retained history. Remove a job from the file and redeploy to remove its schedule and runs. `hostlier cron delete SERVICE/JOB` removes them immediately, and a later deploy recreates the job if it is still declared. Deleting the service removes all its jobs.

If cron reconciliation fails after the service becomes healthy, the operation reports a failure and keeps the healthy service revision. Redeploy to reconcile the jobs. The first deployment with service jobs upgrades controller state to version 5, which requires a controller that supports the service `cron` property.
