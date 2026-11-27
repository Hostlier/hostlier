# Hostlier swarms and on-demand services implementation plan

Every Hostlier service belongs to one named swarm. A swarm groups services and defines who can see, start, and modify them. Each service has its own `.hostlier` definition and lifecycle. Ordinary deployments run continuously. An on-demand definition can be registered with zero running instances, then started as an isolated, callable service for a bounded session. The same lifecycle applies to an ADK agent, a browser assistant, a coding harness, or any compatible service.

## K3s-only direction and migration order

Grouping, CLI discovery and action permissions are implemented; see the [current swarm guide](guides/swarms.md). This document also describes later milestones that are not implemented: on-demand services, MCP, duplicate names across groups and microVM execution. The deployment target is K3s only. Hostlier manages workload objects through its existing Kubernetes adapter; K3s owns its container runtime. Do not add a standalone containerd backend or a new Docker execution path for swarms. The Docker-to-K3s migration remains a separate, explicitly reviewed operation.

Complete and verify the K3s runtime migration before enabling named-swarm workload writes on a server. Metadata backfill into `default` must preserve existing application keys, Kubernetes namespaces, volumes, routes, revisions, and accepted operations. Do not combine runtime migration with workload renaming or namespace movement. Ship swarm grouping, CLI discovery, and scoped permissions first; on-demand sessions and optional microVM isolation are subsequent milestones.

A swarm is a Hostlier ownership and authorization group. Retain the existing namespace-per-application layout instead of moving every service in a swarm into one shared Kubernetes namespace. New objects can carry immutable swarm and service ID labels for ownership and policy selection. Metadata membership alone does not grant network connectivity or user access. Preserve existing network policies on migrated applications and review new connectivity explicitly. Namespace boundaries and labels require actual NetworkPolicy enforcement; they are not themselves traffic isolation. [Kubernetes network policies](https://kubernetes.io/docs/concepts/services-networking/network-policies/)

Keep the existing controller/fleet topology during this change: enrolling a controller does not join its server to another K3s cluster. Workload placement continues to target an enrolled controller using `--on HOST` over its outbound gRPC connection. Any future shared multi-node cluster topology needs a separate ownership and migration design.

## Configuration and identity

Add optional top-level `swarm`, `isolation`, and `execution` fields to the existing project contract. Omitted `swarm` resolves to `default`. Omitted `isolation` selects `container`. Setting `isolation: microvm` requests a reviewed VM-backed Kubernetes runtime profile; reject it until the target K3s installation advertises and verifies that capability. Omitted `execution` preserves the ordinary deployment lifecycle. Isolation applies to both continuous services and on-demand sessions. Sidecars inherit membership from their owning application. Resolve membership and isolation centrally after local package composition. [Project contract](../internal/model/model.go) · [Deployment commands](../internal/cli/command_groups.go)

```yaml
version: 2
name: adk-reviewer
swarm: research
isolation: microvm
workload: service
source:
  image: registry.example/adk-reviewer:1.0
endpoint:
  exposure: private
  port: 8080
health:
  exec: ["/app/healthcheck"]
resources:
  cpu: "2"
  memory: 1Gi
execution:
  mode: on-demand
  maxActive: 4
  maxSessionDuration: 30m
```

Use the existing `resources.cpu` and `resources.memory` fields in both isolation modes. For a container, they remain application limits and support fractional CPU. For a microVM, require explicit whole vCPUs and memory in whole MiB. The example requests two vCPUs and 1 GiB of guest RAM per instance. Guest RAM includes the guest kernel, runtime components, and application. Hostlier accounts for VMM and other host overhead separately when admitting sessions. Reject unsupported values during planning and show the effective allocation in the plan and session status. [Current resource validation](../internal/config/project.go) · [Firecracker machine configuration](https://github.com/firecracker-microvm/firecracker/blob/main/src/firecracker/swagger/firecracker.yaml)

Store an immutable swarm ID and a unique display name, with a reserved `default` ID and name. The current grouping release preserves existing application keys and requires names unique across the target server. A later service-identity migration can provide stable internal service IDs and names unique within a swarm. In that later milestone, resolve `research/adk-reviewer` through a membership index and preserve the existing `--on HOST` selector for fleet targets. Resolve an unqualified service name only in `default` on the selected host. Require an existing swarm and reject conflicting CLI and file selectors. A redeploy resolves the existing service identity and membership. Moving a service is a distinct owner-approved action. Keep `default` permanent and require other swarms to be empty before deletion.

## Upgrade existing deployments

1. Add swarm records and a membership index to controller state. After loading either the legacy JSON state or SQLite snapshot, create `default` and backfill every existing application record into it, including stopped, sleeping, and retained deleted records. Commit the backfill and its migration marker in the same store transaction before enabling swarm APIs or reconciliation. Fail startup if the transaction fails and safely retry on the next boot. [Controller initialization](../internal/controller/controller.go) · [SQLite persistence](../internal/store/sqlite.go)
2. Keep all legacy application keys as runtime keys. Preserve routes, namespaces, volumes, revisions, operation receipts, backups, and resource bindings. Resolve membership through metadata so rollout needs zero workload changes. Before supporting duplicate names, allocate a backend-safe runtime key independent of the user-facing name for new services. The grouping release keeps existing runtime keys for all apps. Carry that key through runtime, telemetry, locks, and ownership checks. Audit bare-name lookups in planning, status, logs, bindings, and lifecycle operations before enabling duplicate names across swarms. [App API](../internal/controller/api_apps.go) · [Deployment records](../internal/controller/deployment_types.go)
3. Preserve existing CLI and API lookups for legacy services and resolve existing exact-app and sandbox-prefix grants only against their original targets in `default`. Treat previously signed plans and accepted operations as legacy-default work under their original scope. Bind newly created plans to service ID, swarm ID, target host and revision. Verify authority again at execution so a stale approval cannot apply after a membership change. Preserve host-specific lookups in mixed-version fleets. Gate named-swarm writes on host capability until the target upgrades.
4. Verify backup and restore with both pre-upgrade and swarm-aware snapshots. Protect the pre-upgrade backup and fence binary downgrade once the controller has written named-swarm state. Make the rollback procedure restore a compatible snapshot and reconcile any operations accepted after that snapshot before opening writes.

## Swarm permissions

Use a single controller authorization check for CLI, MCP, and direct API calls. Extend the current actor-bound grants with an immutable swarm ID, an action set, optional service allowlist, allowed host identities, allowed secret and resource references, expiry, usage limits, and concurrency limits. Membership in `default` conveys zero permissions by itself. Extend the existing action-grant engine instead of creating a second authorization path: swarm ID constrains where `apps.deploy`, `apps.rollback`, `apps.scale`, lifecycle actions, `apps.purge`, and `sidecars.remove` apply. `swarms.manage` authorizes reviewed swarm administration within its granted scope; it must not implicitly authorize deployments, secret use, membership moves into an unapproved destination, or issuance of stronger grants. The table below describes capability groups, not interchangeable permission names. [Scoped access](../internal/controller/access_scope.go) · [Authorization records](../internal/controller/authorizations.go)

| Permission | Allowed within the granted swarm |
| --- | --- |
| Discover | List permitted service definitions and see their allowed metadata. |
| Start | Start a registered on-demand revision or a stopped ordinary service. Inspect, stop, or collect outputs from on-demand sessions the caller owns. |
| Modify | Push or update definitions and apply ordinary deployments through the existing reviewed plan flow. Running an isolated verification session also requires Start. |
| Administer | Manage the swarm and membership; grant issuance remains separate owner/all-access authority. |

Check scope before exposing lists, status, logs, events, result artifacts, or session endpoints, including through legacy routes and gRPC watches. Recheck the principal, grant, revision, membership, and selected host at admission and immediately before provisioning. Reserve concurrency and usage in the same transaction as session creation. Issue a separate endpoint credential scoped to the session. Grant revocation blocks pending starts and leads to revocation and teardown of its live on-demand sessions. Audit grant ID, actor, service, revision, host, and outcome.

Service authors can run code with the privileges available to that service. Require a reviewed revision to change isolation and separate approval to expand secret use, network access, placement, or resource limits. Keep `start` inputs bounded to declared fields and staging references. CLI and MCP starts inherit isolation and sizing from the approved revision. For membership moves, require destination policy review and drain active on-demand sessions before changing membership.

## Register and run a service

1. **Push.** Reuse CLI source preparation and reviewed-plan approval to validate the effective manifest and pin its image and package inputs. Save an immutable on-demand revision without starting compute, allocating a run workspace, or creating an application route. Register later revisions atomically so existing sessions continue on the revision they started with. Extend artifact protection to registered revisions and unfinished sessions so cleanup retains their pinned inputs. [Artifact cleanup](../internal/controller/cleanup.go)
2. **Verify.** Offer an explicit smoke test against a pinned revision. Use a temporary session with bounded resources, a deadline, and test credentials or fixtures. Save the readiness and smoke outcome and reconcile teardown whether the test passes, fails, times out, or is interrupted.
3. **Start.** Persist a session before any runtime mutation. Resolve the exact revision, owner, host, lease deadline, resource budget, and optional staged inputs. Require an idempotency key scoped to the caller, reject reuse with different inputs, and derive backend ownership from the session ID. Return a session ID and `starting` or `ready`. Publish the callable endpoint only after required resources are attached and its readiness probe succeeds.
4. **Serve.** Route requests through an authenticated gateway to the single session identified by its credential. Support a request sequence within the lease. Give each session a fresh writable workspace or desktop profile when declared. Stage local folder inputs explicitly, check archive paths and size, and return patches or files through an authorized download. Use approved network access and keep host mounts, runtime sockets, and controller credentials outside the session.
5. **Close and reconcile.** Explicit stop, deadline, or grant revocation closes admission, drains bounded in-flight work, and finalizes available outputs under a separate retention policy. Remove resources recorded as owned by the session, then verify absence. Use a host-local deadline so a disconnected controller cannot keep an instance running indefinitely. Persist `cleanup_pending` and retry after restart or reconnection. Reconcile an ambiguous start on the original host before considering another instance. Shared services and persistent resources retain their own lifecycle.

The current `sleepAfter` gateway demonstrates request-aware readiness for stateless public HTTP services. Private sessions need an authenticated gateway and per-session ownership. Start with stateless services, bounded scratch storage, and declared access to existing dependencies. Add per-session external provisioning only for integrations that can record ownership, inspect partial creation, and verify removal. [Idle service behavior](guides/scale-and-monitor.md#sleep-an-idle-http-service)

## K3s execution and optional microVM isolation

Use the existing K3s adapter for provisioning, readiness, inspection, and teardown. Ordinary services keep their existing Deployment/Service lifecycle. Registration of an on-demand revision creates no workload or application route. The first on-demand implementation should create a separately owned Kubernetes Job and private Service per session, with bounded scratch storage and explicit resource requests and limits. The Job must not be controlled by an application Deployment or autoscaler that could recreate an expired session. [Current K3s adapter](../internal/runtime/k3s.go)

Use `restartPolicy: Never`, no automatic Job retries, and deadlines derived from the persisted session lease. Kubernetes Job and Pod deadlines provide additional execution bounds; completed-Job TTL is only cleanup, not a running-session deadline. Keep gateway admission tied to the absolute lease expiry, and require a node-local expiry mechanism before claiming teardown survives controller or cluster-control-plane outages. Validate deadline behavior under scheduling delay, node restart, partition, and clock skew. A Job may create replacement Pods, so bind the admitted endpoint to the reconciled Pod UID and refuse a second active session instance until the previous one is accounted for. [Kubernetes Jobs](https://kubernetes.io/docs/concepts/workloads/controllers/job/)

K3s supplies containerd configuration and supports alternative runtimes through Kubernetes RuntimeClass. Use an operator-installed, reviewed runtime profile for any future Kata/Firecracker integration; neither the CLI nor service manifests may select arbitrary host runtime handlers. Pin and verify the supported K3s/Kata/runtime versions, KVM availability, guest artifacts, storage configuration, node eligibility, and runtime overhead. Confirm requested VM CPU and RAM from inside the guest before advertising the capability. [K3s alternative runtime configuration](https://docs.k3s.io/advanced)

Keep microVM support behind a host capability check. If the verified integration cannot honor isolation, sizing, or teardown, return a capability error before creating an instance. Do not fall back to ordinary containers or add a direct Firecracker backend outside K3s. Start with one primary workload per session Pod/VM; sidecars, if supported, share that session's reviewed isolation boundary.

Persist resolved isolation, sizing, runtime profile, node constraints, image digests, and Kubernetes ownership identifiers with each approved revision and session. Planning and admission recheck target capabilities; registration reserves no runtime capacity. Sizing or isolation changes create a reviewed revision and apply to future sessions. Existing sessions remain on their pinned revision. The authenticated gateway, action permissions, output retention, and cleanup reconciliation remain Hostlier responsibilities.

## CLI and MCP interface

Use the same controller operations and structured responses for both clients. Starting an on-demand service accepts a service reference, an optional revision, a bounded lease duration, optional staged input IDs, and an idempotency key. It returns the session ID, pinned revision, deadline, and status. `service_status` returns readiness and the authenticated connection information when available. Return task and teardown outcomes so a caller can retrieve results while cleanup is still being reconciled. Route starts of ordinary services through the existing app lifecycle with swarm authorization.

| Proposed CLI | MCP tool |
| --- | --- |
| `hostlier swarm create research` / `hostlier swarm list` | `swarm_create` / `swarm_list` |
| `hostlier service push --file ./reviewer/.hostlier` | `service_push` with a prepared manifest and staged artifact references |
| `hostlier service verify research/adk-reviewer` | `service_verify` |
| `hostlier service list --swarm research` | `service_list` |
| `hostlier service start research/adk-reviewer --for 15m` | `service_start` |
| `hostlier service status SESSION_ID` | `service_status` |
| `hostlier service stop SESSION_ID` | `service_stop` |
| `hostlier service fetch SESSION_ID` | `service_fetch_result` |

Use `hostlier mcp serve` over stdio with an explicitly selected scoped profile for local clients. A local CLI push can build or read files through the ordinary project preparation flow. MCP tools receive staged references and exercise only the identity of their authenticated caller. For remote callers, add an opt-in Streamable HTTP endpoint with MCP-compatible authorization and pass the resolved caller to the same controller checks. A disconnected client can resume by session ID. Lease termination is an explicit service operation. [MCP transports](https://modelcontextprotocol.io/specification/latest/basic/transports) · [MCP authorization](https://modelcontextprotocol.io/specification/latest/basic/authorization)

## Delivery and acceptance

| Milestone | Release gate |
| --- | --- |
| K3s migration baseline | Complete the runtime migration independently. Verify existing apps, sidecars, storage, routes, backups, restart recovery, and rollback procedures before enabling named-swarm writes. |
| Default swarm and CLI discovery | Upgrade fixtures from legacy JSON and SQLite. Verify running, stopped, sleeping, and deleted services, routes, backups, grants, and accepted operations. Demonstrate restart-safe migration and round-trip restore. |
| Named swarms and scoped grants | Preserve server-wide unique names; reject implicit membership changes. Verify caller isolation across lists, status, logs, plans, artifacts, watches, and lifecycle operations. Exercise grant changes between plan and execution. |
| On-demand CLI on K3s | Push one ADK research service, verify it, then start two isolated sessions. Exercise lost start responses, readiness failure, expiry, controller restart, host disconnect, failed cleanup, and ownership checks. Confirm zero running instances after push and after verified cleanup. Verify Job/Pod ownership, deadline enforcement, lack of unintended retries, and admission of only one reconciled Pod per session. |
| MicroVM isolation and sizing | Start the same harness in a container and a microVM and verify its provenance in both. Verify the isolation mode, configured guest memory and vCPUs, host capacity accounting, per-session teardown, and rejection on an incapable host. Verify sizing changes take effect on a new revision. |
| MCP parity and fleet placement | Run the same flows via MCP with scoped identities. Add a second harness with a different resource profile and test workspace input and artifact return. Apply the existing fleet enrollment and host-pin checks before enabling remote placement. |
