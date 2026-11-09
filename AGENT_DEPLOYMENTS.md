# Agent deployments and sidecars

This historical proposal describes an earlier built-in Harnest interface. The current implementation uses the separately installed [Harnest project package](docs/guides/package-project.md).


Status: the core version 2 implementation is available on Docker and k3s. See [the implementation guide](docs/agent-deployments.md) for the supported syntax, verified cases, and current limits. This document retains the broader design and proposed extensions; examples and acceptance items below are not a blanket claim that every proposal has shipped.

Hostlier will deploy ordinary applications and agents through the same infrastructure lifecycle. Users can supply a container image or a compiled Harnest artifact, choose public or private access, and attach an agent or application as a sidecar to an existing Hostlier application. Hostlier owns deployment and infrastructure; the agent runtime owns agent execution, session state, checkpoints, and tool behavior.

This extends the [Hostlier proposal](PROPOSAL.md). It preserves the existing reviewed-plan, immutable-revision, ownership, and data-retention contracts. It does not change the completion status of earlier acceptance gates.

## Required Docker and k3s support

Docker and k3s are both required deployment backends for this extension. Image deployments, compiled Harnest artifact deployments, public and private services, workers, and sidecar attachment/update/detachment must work on both. Neither backend is a deferred follow-up. A feature is complete only after its applicable acceptance cases pass against both real backends.

Use one user-facing configuration and lifecycle contract with backend-specific implementation underneath. Both backends must preserve loopback sidecar access, private exposure, scoped secrets, readiness, drain behavior, revision ownership, rollback, and data retention. Differences in restart strategy, capacity enforcement, or scheduling must be shown in the plan. Capability checks cannot substitute for implementing these required features on either backend; they apply to optional extensions and unsupported configurations. Existing separately planned scaling limits remain explicit.

k3s artifact deployment must use its containerd runtime without requiring Docker Engine on the server. Docker deployment must work without installing k3s. Each implementation phase below includes both backends and their real-runtime verification before it is considered complete.

## Deployment model

Keep packaging, process behavior, and connectivity separate. An agent is an application with additional runtime capabilities, not a separate infrastructure backend.

| Dimension | Choices | Meaning |
| --- | --- | --- |
| Source | Registry image, compiled Harnest artifact | How executable content arrives |
| Workload | Service, worker; later job and scheduled job | Whether it serves requests, continuously consumes work, or runs to completion |
| Exposure | Public, private, none; sidecar loopback | Who can connect to a listener |
| Placement | Independent application or sidecar | Whether lifecycle and replica placement are independent or shared |
| Runtime profile | Generic, Harnest; later framework presets | Optional conventions for launch, health, drain, and telemetry |

A service has a declared listener and readiness check. A worker requires neither a public route nor a listening port. A sidecar can be any compatible app or agent and can itself be a service or worker. In-process Harnest SubAgents remain inside their parent runtime; they do not automatically become containers or sidecars.

### Access modes

| Mode | Address used by caller | Public route | Lifecycle |
| --- | --- | --- | --- |
| Public service | HTTPS hostname/path | Explicit Caddy route | Independent application |
| Private service | Hostlier-managed private DNS and port | None | Independent application |
| Sidecar service | `http://127.0.0.1:<port>` from its application group | None for the sidecar | Shared application revision and replica count |
| Worker | No inbound endpoint | None | Independent or part of an application group |

Private means no public Caddy route, host port publication, NodePort, or LoadBalancer. Private services explicitly grant access to named Hostlier consumers. No route prompt, DNS prerequisite, or certificate issuance applies to them. Private ingress does not disable the outbound access an agent needs for model providers and tools.

Loopback is relative to a network namespace. Host loopback, a container's loopback, and a Pod's loopback are different. Attaching two containers to the same Docker bridge alone does not make `127.0.0.1` shared. Docker supports joining a container network stack; Kubernetes containers in one Pod share networking. See [Docker networking](https://docs.docker.com/engine/network/) and [Kubernetes Pods](https://kubernetes.io/docs/concepts/workloads/pods/).

Host-loopback access for an administrator is a separate, explicit capability. A future authenticated port-forward command can expose a temporary local listener; it must not silently become a permanent publication mechanism. Containers outside an application group use private service discovery, not the server's loopback address.

## Sidecars and existing applications

An application group contains one primary workload and zero or more sidecars. Every member has its own source digest, command, resource limits, secret bindings, health state, and logs. Members share networking and only the volumes explicitly mounted into each member.

- Sidecar services bind to loopback. Only the primary's declared public listener is routed through Caddy.
- Ports are unique across the group, including health and management listeners. Validate declared conflicts before mutation and report runtime bind failures without activation.
- Sidecars receive only their own declared secrets. They do not inherit the primary's environment or credentials.
- A sidecar is part of the group's trust boundary. Shared networking does not isolate members from one another, and network policy cannot independently restrict their egress. Use a separate private service when that isolation is needed.
- Hostlier never grants a Docker socket, host networking, host filesystem access, or shared process namespace merely because a workload is a sidecar.
- Group replicas scale together: three primary replicas mean three copies of each sidecar. A shared agent serving several apps should be an independent private service.
- A required sidecar must become ready before group activation. Failure removes the group from readiness; optional sidecars report degraded state without automatically withdrawing the primary.
- Readiness gates traffic, not arbitrary startup calls from the primary. Apps must retry startup connections unless an explicit, backend-supported startup dependency is declared. Reject cycles and unsupported ordering.

### Attaching to an existing app

Attachment produces a new revision of the existing application group. Resolve the parent by its Hostlier identity, retain its current effective configuration, add the sidecar, and show the complete diff. Bind the plan to the parent revision so concurrent updates cannot overwrite one another.

The plan includes the primary and sidecar digests, new ports, bindings, resource totals, readiness effects, volume changes, and restart or downtime implications. Apply through the existing approval flow. Do not mutate an unrelated or unmanaged container. Adopting unmanaged workloads is separate future work.

On both backends the initial contract is a group rollout. Adding a sidecar to an existing Kubernetes Deployment changes its Pod template and replaces Pods. Docker uses a managed group network namespace and can require recreating the primary when migrating an existing app into a group. Do not promise a live attachment with no restart.

Updating or detaching a sidecar also creates a group revision. Rollback restores the recorded primary and sidecar set, their bindings, and compatible configuration together. Persistent data remains retained; rollback does not undo database migrations or external agent actions. Deleting the parent removes its owned sidecar workloads, while ordinary deletion retains persistent data. Lifecycle commands on a sidecar cannot accidentally delete the parent.

### Backend mapping

| Concern | Docker | k3s |
| --- | --- | --- |
| Group networking | Owned infrastructure container holds the network namespace; primary and sidecars join it | Primary and sidecars are containers in one Pod |
| Public ingress | Publish only the primary listener on a private host binding owned by the group; Caddy routes to it | Service targets only the primary port; Caddy routes to that Service |
| Sidecar loopback | No publication of the sidecar port; bind inside the shared namespace | Bind sidecar listener to Pod loopback; no Service for that port |
| Private independent service | Dedicated owned networks and private DNS aliases for approved consumers | ClusterIP/DNS plus verified ingress and egress NetworkPolicy |
| Sidecar health | Probe inside the shared namespace | Exec probe inside the container for a loopback-only listener |
| Failure recovery | Reconcile namespace owner and member dependencies after daemon/host restart | Reconcile the recorded group revision and Pod readiness |

A node-originated Kubernetes HTTP probe cannot be assumed to reach a loopback-only listener. Require a suitable exec probe or a profile-provided probe executable, and validate its availability. Do not broaden a private listener to make an existing HTTP probe work. Generic images can supply their own probe command; Hostlier must not assume that a shell, curl, or Python is present.

Docker private networks grant reachability to their members, not fine-grained per-port authorization. Show that capability in the plan and retain application authentication where callers need separate permissions. Do not claim k3s policy enforcement until tested on the installed network implementation. Test IPv4 and IPv6 wherever enabled.

## Proposed project contract

Introduce project schema version 2. Preserve version 1 behavior, including its public-route requirement. Internally normalize both versions into the same workload model; older clients and servers reject unsupported versions explicitly.

In version 2, source and workload type are explicit. Exposure defaults to `none`, with public access requiring an explicit route. `image` and `harnest` sources are mutually exclusive. Sidecars inherit group placement and replica count, never public exposure. Unknown fields fail validation.

The following is a proposed full desired-state file for an existing web app with a compiled Harnest sidecar:

```yaml
version: 2
name: storefront
workload: service
source:
  image: ghcr.io/acme/storefront:1.0
endpoint:
  port: 3000
  exposure: public
  route: shop.example.com
health:
  readiness:
    http: {path: /health}
bindings:
  ASSISTANT_URL: sidecars.assistant.url

sidecars:
  assistant:
    workload: service
    profile: harnest
    source:
      harnest:
        artifact: ./dist/support-agent
        format: executable
    endpoint:
      port: 1907
      exposure: loopback
    required: true
    secrets:
      MODEL_API_KEY: secret://support-model-api-key
    bindings:
      DATABASE_URL: dependencies.agent-db.url
    resources:
      cpu: "0.5"
      memory: 512Mi
    shutdown:
      graceSeconds: 120

dependencies:
  agent-db:
    type: postgres
    source: existing
    instance: support-state
```

`sidecars.assistant.url` resolves to `http://127.0.0.1:1907` in the primary. The Harnest profile generates a compatible serve command, loopback bind, and probe; generic images must provide the necessary command/configuration themselves. `MODEL_API_KEY` is illustrative: bind the credential variable consumed by that agent. A database binding supplies a connection string; it does not rewrite the agent's storage lifecycle.

An independently deployed private agent uses a different endpoint declaration:

```yaml
version: 2
name: shared-assistant
workload: service
source:
  image: ghcr.io/acme/support-agent:1.0
endpoint:
  port: 1907
  exposure: private
  consumers: [storefront, admin]
health:
  readiness:
    http: {path: /healthz}
shutdown:
  graceSeconds: 120
```

Private consumers obtain the endpoint through an application reference binding, such as `applications.shared-assistant.url`. Consumer authorization and reference tracking must agree before granting access. References block accidental deletion of a service still in use. Public routes, private names, and sidecar ports are separately scoped ownership records.

The schema also needs command/argument overrides, startup/readiness/liveness probes, restart policy, shutdown controls, per-member limits and secrets, explicit volume mounts, and optional replica settings. Workers use exec/process health or a runtime heartbeat; process existence alone is not advertised as queue progress. Jobs later add exit status, bounded retries, deadlines, and schedule concurrency policies rather than reusing a service's always-restart policy.

The full project file remains authoritative. A CLI attachment action creates a durable revision and offers the resulting configuration for export. A later deploy from an older file must show sidecar removal in the plan; it cannot silently discard or preserve a sidecar contrary to the reviewed desired state.

## Compiled Harnest artifacts

Accept compiled artifacts directly without requiring a user-authored Dockerfile or a separately managed image build. Internally package them as immutable OCI images and use the same Docker/k3s adapters as image-based apps.

| Harnest output | Required input handling |
| --- | --- |
| Compiled directory | Include its matching production runtime and dependency closure |
| Executable with attached runtime | Upload the executable and complete runtime pack; validate the required digest |
| Executable with embedded runtime | Validate platform compatibility and provide a writable extraction/cache location |

Harnest artifacts target an OS, architecture, and compatible runtime baseline. A macOS executable cannot be deployed to Linux. A native executable is not necessarily a static binary, so a scratch image is not a universal base. Validate the supported Linux baseline and system libraries. Reject unsupported artifacts before deployment; do not silently compile source or download application dependencies at startup.

The artifact pipeline must:

1. Inspect bounded metadata without executing uploaded code. Validate format, Harnest/runtime versions, platform, entrypoint, runtime coverage, and safe archive paths and links.
2. Upload resumably into an authenticated staging store with content hashes, size limits, ownership, and retention. Never include deployment credentials in the payload or image layers.
3. Assemble the image from a digest-pinned compatible base, artifact, and runtime pack. Keep packaging separate from the privileged controller process; do not execute agent hooks during packaging.
4. Resolve and retain the final OCI digest before deployment approval. Approval covers that exact image, artifact/runtime identities, configuration, secrets references, and backend distribution changes. Content changes invalidate the plan.
5. Import the image into the selected runtime's image store. For the initial single-server backends, use a capability-checked local import path; k3s must load containerd without requiring Docker. Multi-node distribution later requires explicit registry or node-distribution support.
6. Retain artifact and image references for active, pending, and rollback revisions. Garbage collection cannot remove content still referenced by those revisions. Verify cached content before reuse.

Content staging and packaging prepare a reviewable deployment; they do not start the application or expose a listener. Quotas and cleanup apply to abandoned preparations. A successful upload is not a successful deployment. Source builds and a hosted registry remain separate scope from packaging an already compiled artifact.

The first Harnest profile supplies launch, probe, cache, and server-configuration conventions. It discovers capabilities by a versioned contract, not by guessing from filenames. Shared runtime packs can reduce distribution storage, but each agent remains an isolated process/container; writable caches must not allow one app to alter another's runtime.

## Agent execution and operations

| Responsibility | Hostlier | Agent runtime or application |
| --- | --- | --- |
| Deployment | Immutable revisions, supervision, rollout, rollback, limits | Compatible executable and startup behavior |
| State | Durable databases, volumes, bindings, backup operations | Sessions, checkpoints, queued runs, approval state, schema compatibility |
| Shutdown | Withdraw traffic, signal drain, enforce grace period, report timeout | Stop claiming new work; finish or checkpoint active runs; acknowledge drain when supported |
| Retries | Recover deployment operations without duplicating infrastructure | Lease work, deduplicate requests, make external side effects idempotent |
| Access | TLS, private networks, scoped secret delivery | Caller authentication, session authorization, tool permissions |
| Observability | Workload logs/health and telemetry collector integration | Run IDs, model usage, latency, queue depth, task outcomes, tool audit events |
| Budget | Surface configured resource and runtime policies | Enforce model-token/spend and tool-call budgets through a runtime or gateway |

Container restart does not restore an agent's execution state. Durable deployment records do not make agent runs durable. Multiple replicas require shared stores and a runtime that coordinates ownership. Do not scale to zero when the runtime needs resident queue workers, scheduled work, or active stream handling without a supported wakeup mechanism.

For ordinary updates, remove the old group from new traffic, request drain, and preserve sufficient execution time before termination. Workers must stop acquiring new jobs. Where requests drain through a primary into its sidecar, keep the sidecar available until the primary has drained. Provide backend-tested ordering or report the unsupported requirement during planning. Expiry is reported as forced termination; it is not a claim that all runs checkpointed successfully.

Agent shutdown hooks are optional capabilities. Generic workloads receive their configured termination signal and grace period. Harnest-specific drain behavior must be implemented and verified against the runtime before Hostlier advertises it. Run cancellation, pending approval controls, and checkpoint compatibility checks likewise require a supported runtime API.

Updates involving persistent single-writer volumes retain stop/start semantics. Stateless groups can use candidate readiness and route cutover when the backend supports it. Rolling back executable code requires a compatible state schema; destructive schema changes and already completed external actions are outside deployment rollback.

Verify SSE and WebSocket behavior through the actual proxy, including buffering, idle timeouts, client disconnects, reconnects, and rollout drain. Network streams cannot migrate transparently between processes; runtimes that support resumable events must persist cursors and replay state.

For user-supplied code execution or browser tools, integrate an explicit sandbox provider or isolated execution service. Do not mount the host Docker socket into an agent by default. Record sandbox access, credentials, and required reachability in the plan. Model APIs normally need outbound access; GPU scheduling is a separate capability for locally served models, not a prerequisite for agent hosting.

## CLI and controller behavior

Keep the existing plan/deploy/status/logs/lifecycle flow. Add component selection and artifact preparation without creating a second deployment authority.

Proposed commands include `hostlier logs storefront --component assistant` and `hostlier sidecar attach storefront --file assistant.hostlier`. An attachment file supplies one sidecar definition; the controller constructs and reviews the complete parent revision. `hostlier sidecar detach storefront assistant` plans the corresponding group update. These names are interface proposals, not current commands.

Status returns desired and observed group/member state, source and runtime digests, health, active/draining runs when available, and access endpoints with their scope. A private service or worker returns no public URL. Sidecar loopback URLs are labelled as reachable from the application group, not presented as browser links on the operator's laptop.

Plans display aggregate group capacity as well as per-member limits, public/private access, approved consumers, source identities, runtime capabilities, lifecycle effects, and retention. Parent changes, bindings, secret revisions, artifact digests, and access grants participate in the approval fingerprint. Resource and application references prevent destructive lifecycle actions while dependents remain active.

Accepted operations remain detached from the CLI. Persist per-member creation, readiness, activation, drain, and cleanup records. Recover owned resources after interruption without spawning duplicate sidecars or losing the prior revision. Partial failure must retain useful component diagnostics. Group revision commit is atomic metadata, not a promise that external infrastructure mutations or database changes are globally atomic.

## Implementation sequence

1. **Workload and exposure model.** Add v2 normalization, private services, workers, optional route/port behavior, richer probes and shutdown settings, structured endpoint results, and capability checks on both backends. Preserve v1 behavior.
2. **Application groups and sidecars.** Add member ownership/state, Docker namespace management, k3s multi-container Pods, component logs, endpoint bindings, attach/detach plans, and group rollout/rollback. Include conversion of existing apps and visible downtime.
3. **Harnest artifact deployment.** Add staging, metadata validation, OCI assembly, runtime import, content retention, and the versioned Harnest launch/health profile. Start with one embedded executable, then attached packs and compiled directories.
4. **Production agent behavior.** Validate durable-store bindings, drain integration, streaming, restart recovery, telemetry, and explicit sandbox capabilities. Add verified image presets for other frameworks without coupling the core controller to their SDKs.
5. **Scale and scheduling.** Integrate the existing planned replica/scaling contract, independent private worker scaling, jobs, scheduled jobs, queue metrics, and runtime-aware admission. Reject unsupported backend features rather than silently changing semantics.

Implementation touches `internal/model`, `internal/config`, CLI/client contracts, controller plans/revisions/operations, both runtime adapters, Caddy route selection, and resource/application reference tracking. Add a separate artifact-packaging boundary. Keep image/framework-specific dependencies outside the core controller where practical.

## Acceptance criteria

Each applicable case must pass against real Docker and k3s installations. Unit tests cover schema and ownership invariants but do not establish networking or process behavior.

| Case | Required result |
| --- | --- |
| Existing v1 app | Route selection, HTTPS, lifecycle, secrets, and rollback retain existing behavior |
| Public agent image | Real invocation and streaming succeed using declared credentials and backing services |
| Private service | Approved consumers connect through private discovery; unrelated workloads and external clients cannot connect; no public route or host publication exists |
| Worker | Starts without route/port, exposes useful health, and follows its restart/drain contract |
| Sidecar attachment | Existing app gains a sidecar through a reviewed revision; localhost invocation works; planned restart/downtime matches observation |
| Sidecar isolation | External and unrelated workloads cannot reach the loopback listener; only declared secrets and volumes reach each member |
| Port and probe validation | Conflicts and unavailable probes fail clearly; loopback readiness works without widening listener exposure |
| Sidecar failure | Required and optional sidecars produce their specified readiness/degraded behavior; recovery does not duplicate members |
| Group update and rollback | Primary and sidecar versions, bindings, routes, and retained data match the approved revision; failed candidates do not replace healthy service |
| Attachment race | A parent update after planning invalidates the old attachment plan; retries identify the original operation |
| Reboot and interrupted apply | Ownership and intended topology survive controller/runtime/host restarts; recovery is explicit and preserves data |
| Artifact compatibility | Wrong OS/architecture, incompatible runtime packs, corrupt payloads, unsafe archive paths, and stale digests are rejected before workload activation |
| Artifact retention | Active and rollback revisions remain launchable after preparation cleanup; abandoned artifacts can be collected safely |
| Graceful shutdown | New work stops; supported runs drain/checkpoint; timeout is observable; sidecars remain available to the draining primary |
| Agent durability | Session/checkpoint/queue state survives forced process loss; retries do not duplicate the test's external effect under the runtime's documented idempotency contract |
| Streaming | SSE/WebSocket behavior, disconnect handling, and rollout interruption are measured and documented |
| Replicas and state | Every primary gets its sidecars; shared stores coordinate work; unsupported volume or backend scaling is rejected |
| Removal and stale files | Detach/delete respects ownership and retention; deploying an older desired-state file visibly plans any sidecar removal |

## Supporting references

- [Current Hostlier project model](internal/model/model.go), [runtime interface](internal/model/runtime.go), and [deployment revision records](internal/controller/deployment_types.go).
- [Docker deployment lifecycle](internal/runtime/deployment.go) and [k3s workload construction](internal/runtime/k3s.go).
- [LangChain standalone Agent Server deployment](https://docs.langchain.com/langsmith/deploy-standalone-server) for an image-based agent with database, queue, and drain requirements.
- Harnest's compilation, production serving, provisioning, and durable execution contracts define the artifact formats and runtime capabilities consumed by its integration. Verify the supported Harnest release as part of implementing the profile.
