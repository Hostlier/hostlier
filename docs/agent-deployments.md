# Deploying applications, agents, and arbitrary sidecars

Hostlier project version 2 supports image-based applications and agents, private services, listenerless workers, and sidecars on **Docker and k3s**. Version 1 projects retain their existing behavior. The broader design remains in [AGENT_DEPLOYMENTS.md](../AGENT_DEPLOYMENTS.md); this guide describes the implemented interface.

An application group has one primary workload and zero or more arbitrary sidecar workloads. A sidecar can be an ordinary HTTP application, a proxy, a background worker, or an agent. There is no agent type requirement: `source.image`, explicit launch/probe settings, and the normal service/worker contract are sufficient.

## A generic application with service and worker sidecars

The runnable [sidecars example](../examples/sidecars.hostlier) contains a public web application, a private HTTP backend on loopback port 8081, and a listenerless background worker. All three use ordinary BusyBox images; no agent framework, model provider, or Harnest compiler is involved.

```sh
hostlier validate --file examples/sidecars.hostlier
hostlier deploy --file examples/sidecars.hostlier --route sidecars.apps.example.com
hostlier status sidecar-demo
hostlier logs sidecar-demo --component backend
hostlier logs sidecar-demo --component worker
```

Replace the route with a hostname configured on your server. The same project works on Docker and k3s; image tags are resolved to immutable digests during planning. The primary's `BACKEND_URL: sidecars.backend.url` binding resolves to `http://127.0.0.1:8081`. Each named sidecar has its own image, command, environment, credentials, resource limits, health probe, and optional retained volumes. Only the primary's public port is routed.

Sidecars are long-running members of the application group. They share its placement, network namespace, lifecycle, and replica count, but do not automatically share filesystems or process namespaces. Required members become ready before the primary starts, including Docker start/restart. An ordinary app that needs an independently scaled lifecycle should instead be deployed as a private service. One-shot init containers and scheduled jobs are outside this sidecar contract.

## Package a Harnest agent

Install the Harnest adapter locally from the Hostlier Git catalog, using the commit returned by discovery:

```sh
catalog=https://github.com/Usefused/hostlier-packages.git
hostlier package available --repository "$catalog"
hostlier package install harnest --repository "$catalog" --version COMMIT_FROM_LIST
hostlier init
hostlier package add harnest
```

Set the inputs in `packages.harnest` in the generated `.hostlier`. In a new project, run `hostlier init --package harnest` to create the starter agent. Run `hostlier plan` and `hostlier deploy` from the project directory. The adapter checks the agent and produces a container build context. See [project package hooks](guides/package-project.md).

## Attach an arbitrary app or worker to an existing app

A sidecar file describes one component. For example, this generic image supplies its own probe executable:

```yaml
version: 2
source:
  image: registry.example/backend:version
workload: service
endpoint:
  exposure: loopback
  port: 8081
health:
  exec: ["/app/probe", "http://127.0.0.1:8081/healthz"]
shutdown:
  graceSeconds: 30
secrets:
  BACKEND_API_KEY: secret://backend-api-key
```

```sh
hostlier sidecar attach storefront --name backend --file backend.yaml
hostlier logs storefront --component backend
hostlier status storefront
hostlier sidecar detach storefront --name backend
```

The image and probe above are placeholders for your application's actual image and executable. Use an ordinary container image for a sidecar. Every private service and worker requires an explicit `health.exec`; Hostlier does not assume the image contains a shell, curl, or Python.

Attachment upgrades a version 1 app to version 2 while preserving its public route. Attach/detach creates a reviewed deployment of the complete group. Updates use the complete project with a `sidecars` map; rollback restores the whole retained revision. A referenced member cannot be detached while its endpoint binding remains in the desired configuration.

The primary reaches this sidecar at `http://127.0.0.1:8081`. In a full group project, `bindings: {BACKEND_URL: sidecars.backend.url}` supplies that address. Docker uses an owned namespace-holder container; k3s uses a shared Pod and native sidecars. Sidecar ports are not published. Port collisions and nested sidecar groups are rejected. To attach a listenerless worker, declare `workload: worker`, `endpoint: {exposure: none}`, its command, and an explicit exec probe in the member file.

Members receive their own declared environment, secrets, resource limits, and volumes. They do not inherit the primary's credentials or mounts. Required sidecars start and become ready before the primary; the primary drains before sidecars stop. `required: false` removes a member's readiness gate after its process can start. Sidecars remain part of the parent's placement and replica count.

## Private independent services and workers

A private service uses `endpoint.exposure: private`, a port, an exec readiness probe, and `endpoint.consumers: [storefront]`. It has no public route or certificate. An authorized consumer declares `bindings: {AGENT_URL: applications.assistant.url}`. Docker connects the approved group to the provider's private network; k3s uses cluster DNS and NetworkPolicy. Docker network membership grants network-level reachability, rather than per-port authorization.

Active consumer bindings prevent provider stop/deletion, port changes, grant revocation, and scaling to zero until consumers are stopped or redeployed. Bindings inside sidecars count as references owned by the parent application.

A worker uses `workload: worker`, no port, `endpoint: {exposure: none}`, and `health.exec`. Its command runs continuously under normal deployment supervision. One-shot and scheduled jobs are not implemented.

## Current limits

- Artifact import targets a single local Docker or k3s server. Distribution of local artifacts across a multi-node cluster is not implemented.
- Prepared image archives have no fixed byte cap. Uploads use 1 MiB chunks, and the controller preserves at least 256 MiB or 5% of the artifact filesystem as free space. Current deployments, retained rollback revisions and active plans protect their artifacts; expired unreferenced artifacts can be reclaimed by controller cleanup.
- k3s must support native sidecars and enforce NetworkPolicy. Docker and k3s retain their existing explicit replica limits. A group with any persistent volume is limited to one replica.
- Group updates may restart the primary. Changes involving retained writable volumes require a maintenance window; image rollback does not roll back application data.
- Hostlier supervises processes and readiness. Agent session recovery, checkpointing, model budgets, and stopping or resuming individual agent runs remain application responsibilities.

## Verification

On 29 October 2026, `make check` passed: the source-quality gate, `go vet ./...`, and `go test -race ./...`.

The generic `TestAgentGroups` fixture covers shared-loopback invocation, member secret separation, rollback, restart, workers, private services and cleanup. To run it on a disposable Linux server:

```sh
HOSTLIER_AGENT_E2E=docker HOSTLIER_CADDY_BINARY=/usr/bin/caddy \
go test ./test/e2e -run '^TestAgentGroups$' -v -timeout 15m
```

Repeat with `HOSTLIER_AGENT_E2E=k3s` for the Kubernetes backend.
