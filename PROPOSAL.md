# Hostlier proposal and implementation plan

Hostlier lets a user prepare their own server and deploy containerized applications through a small CLI. The user supplies a Linux server and configures DNS. Hostlier installs the required infrastructure, runs applications, allocates routes, manages HTTPS, and returns working URLs.

Docker is the default runtime for a simple installation. Caddy handles routing and certificates. Iteration 1 delivers the controller and CLI together. Iteration 2 adds an optional k3s backend and first-party PostgreSQL, Redis, and Neo4j instance provisioning. Custom products built with the Hosting SDK arrive in iteration 3.

This is the product and acceptance contract. The implementation is a standalone Go CLI/controller, built as one executable without a Node.js runtime dependency. See [README.md](README.md) for implemented commands and [IMPLEMENTATION.md](IMPLEMENTATION.md) for architecture and recovery behavior.

The accepted extension direction for agents and sidecars is specified in [Agent deployments and sidecars](AGENT_DEPLOYMENTS.md): image and compiled Harnest inputs, public/private services, workers, and application groups with loopback sidecars. The core version 2 project contract is implemented; [the deployment guide](docs/agent-deployments.md) records supported syntax, backend verification, and remaining limits. The design retains its broader implementation and acceptance sequence. Earlier iteration boundaries remain unchanged; artifact packaging extends the image input path without introducing general source builds.

The agent and sidecar extension requires both Docker and k3s support. Its required features are complete only after verification on both real backends; neither backend may be deferred as follow-up work.

The user-selected verification scope for this delivery is local-only: real Docker/Caddy with a trusted local CA, database protocol/data tests, and disposable Ubuntu VM checks. Public DNS and public ACME issuance/renewal are not being exercised or claimed. Full iteration completion still requires the acceptance gates below; local test results must identify which cases remain unverified, including the complete k3s parity matrix.

## Product boundaries

- Applications, the controller, deployment state, and secrets live on the user's server. A central Hostlier cloud service is not required.
- The CLI runs on the server or a developer's laptop. Accepted deployment jobs continue after the CLI disconnects.
- Users manage DNS themselves. Hostlier validates routing prerequisites and configures the server side of domains and paths.
- `init` explains the proposed server changes and requires approval before applying them, including installation, startup services, and necessary host firewall rules.
- Every new deployment, redeployment, update, or rollback requires approval of its concrete plan in the CLI. Pairing a client or approving server initialization does not authorize future deployments.
- Docker and k3s are alternative runtimes selected for a server. k3s uses its bundled containerd; it does not require a separate Docker Engine installation.
- The initial model is a trusted server owner and their authorized operators. Untrusted tenant hosting and billing are outside these iterations.
- Iterations 1–2 excluded tunneling and Nginx. Both have since shipped: optional Nginx routing in iteration 3 (see [Nginx coexistence](docs/nginx-coexistence.md)) and HTTPS tunnels through [Fused Bridge](docs/guides/bridge-tunnels.md).
- Source builds, a hosted image registry, automatic runtime migration, and multi-server scheduling are outside the first two iterations. Deployments consume images from registries.

## Architecture and ownership

| Component | Responsibility | Location |
| --- | --- | --- |
| Hostlier CLI | Setup, server selection, validation, planning, deployment, status, logs, and lifecycle commands | Laptop or server |
| Hostlier controller | Durable operations, application records, secret references, route ownership, runtime coordination, and recovery | User's server |
| Docker Engine | Containers, private networks, resource limits, and volumes in the default runtime | User's server |
| Caddy | Public HTTP/HTTPS, certificate renewal, host and path routing | User's server |
| k3s | Optional Kubernetes workload and storage backend in iteration 2 | User's server |
| Hosting SDK | Reusable lifecycle primitives where suitable; custom product authoring in iteration 3 | Library used by Hostlier and product authors |

The controller manages infrastructure through adapters. Application traffic flows through Caddy to the application's private endpoint, rather than through the controller API. Caddy also exposes a dedicated HTTPS hostname for authenticated controller requests. Docker's socket, Caddy's administration interface, and the controller's local socket remain private.

For iteration 1, remote CLI commands connect to the controller over HTTPS after explicit token-based pairing. Pairing can start on the controller or on the CLI, as described below. Routine CLI operations do not require SSH credentials. Local administration uses a permission-protected controller socket, including initial pairing and recovery if remote access is lost. Use one server-side state directory, proposed as `/var/lib/hostlier`, with durable operation records and protected secret storage. Caddy certificate state and application volumes also persist across restarts.

Reuse suitable Hosting SDK state, operation, validation, and recovery code after checking its dependencies. Do not make the existing dashboard, Kubernetes setup, or custom-product execution mandatory for a basic Docker installation. Hostlier's CLI and controller need their own small distribution and installation lifecycle.

## Configuration

### Server configuration

`/etc/hostlier/config.yaml` defines the domains supported by this server. It does not configure whether a domain supports subdomains or paths, or choose a routing mode for applications. Those decisions belong to each deployment, through CLI flags or `.hostlier`.

```yaml
version: 1
domains:
  - apps.example.com
  - tools.example.org
  - example.com
```

Each entry makes the listed hostname and subdomains allocated beneath it available for deployments. Domain matching must respect DNS label boundaries; `example.com` must not authorize `notexample.com`. Hostlier still enforces individual route ownership and reserved controller hostnames.

The user configures DNS for the hostnames they actually deploy. For example, `apps.example.com` and `*.apps.example.com` can point at the server to cover the base hostname and generated subdomains. A wildcard DNS record permits allocating subdomains without further DNS edits. Listing a domain does not create DNS records or prove that a selected hostname resolves correctly. Paths do not require separate DNS records.

Hostlier explicitly configures each allocated hostname in Caddy and obtains an individual certificate. Wildcard DNS does not imply a wildcard certificate. This avoids requiring DNS provider credentials. DNS and public certificate-validation access must work before an HTTPS deployment is reported ready.

Custom hostnames can be registered as additional domain entries and attached to applications. Configuration changes are validated before activation. Removing a domain with active routes must fail until its routes are explicitly moved or removed. Reserve the controller hostname so applications cannot claim it. By default, `init --domain apps.example.com` registers that supported domain and selects `hostlier.apps.example.com` for the controller; `--controller-hostname` can override the controller address. Its DNS and certificate must be ready before remote pairing is offered, while local administration remains available.

The runtime selection and controller address are installation settings persisted in controller-managed state. They are not fields in this domain-focused `config.yaml`. Docker remains the installation default; iteration 2 permits choosing k3s with `init --runtime k3s`.

### Project configuration

The CLI reads a YAML file named `.hostlier` in the current directory. `--file` selects another path; implicit parent-directory discovery is excluded initially to avoid deploying the wrong project.

```yaml
version: 1
name: analytics
image: ghcr.io/acme/analytics:1.0
port: 3000

route: analytics.example.com

health:
  path: /health

resources:
  cpu: "0.5"
  memory: 512Mi

env:
  NODE_ENV: production

secrets:
  API_KEY: secret://analytics-api-key

volumes:
  uploads:
    mount: /app/uploads
    size: 5Gi
```

`route` is a single address containing a hostname and an optional path. Hostlier parses it, validates the hostname against the server's supported domains, reserves the hostname/path combination, configures Caddy, and manages HTTPS. Users do not split the address into domain, subdomain, and path fields.

| Route value | Public URL |
| --- | --- |
| `analytics.example.com` | `https://analytics.example.com` |
| `example.com/analytics` | `https://example.com/analytics` |
| `analytics.example.com/reports` | `https://analytics.example.com/reports` |
| `example.com` | `https://example.com` |

Accept either a scheme-less address or an explicit `https://` URL. Normalize hostnames and equivalent root addresses before ownership checks. Reject unsupported schemes, embedded credentials, explicit ports, query strings, fragments, and malformed or ambiguous paths. A path is a segment-aware mount prefix, not a regular expression. Hostname/path collisions fail explicitly instead of replacing another application's route.

If neither `.hostlier` nor `--route` specifies a route, an interactive deployment asks for the full address. It may suggest an application-name subdomain beneath a user-selected supported domain, but the user confirms the complete address before provisioning. Noninteractive deployment fails with an actionable message instead of choosing the first domain or publishing an unintended route. Planning must resolve and show the same route that deployment will use, and accepted deployment records retain the resolved address.

Volume `size` expresses requested capacity. The backend must report whether it can enforce a quota; ordinary Docker volumes must not be presented as having an enforced size limit. Scheduling, limits, and storage semantics must be visible in `hostlier plan` when they differ between backends.

Path routing uses the same field:

```yaml
route: example.com/analytics
```

Path-based applications must support their configured public base path. Hostlier handles segment-aware matching and redirects from the bare mount path where needed. It preserves the path prefix by default. An optional `proxy.stripPrefix: true` setting controls upstream rewriting for applications that require it; this does not change the public address. Hostlier does not rewrite arbitrary application HTML, cookies, or redirects to make every application work under a prefix.

Equivalent choices are available directly in the CLI:

```bash
# Subdomain: https://analytics.example.com
hostlier deploy --route analytics.example.com

# Path: https://example.com/analytics
hostlier deploy --route example.com/analytics

# Exact hostname at its root: https://tools.example.org
hostlier deploy --route tools.example.org
```

`--route` replaces the `.hostlier` route for that operation without editing the file. Deployment commands use this one address option instead of separate `--domain`, `--subdomain`, or `--path` flags; `init --domain` still registers a supported server domain. `hostlier plan` accepts the same route override and shows the effective URL and source of the choice. Prefix stripping is permitted only for a non-root path; an override that makes it invalid fails validation. Controller-side validation uses the same address parsing and rejects unsupported domains regardless of whether the request comes from the CLI or another API caller.

## Iteration 1

### Outcome

On a fresh supported Linux server, the user can install Hostlier, connect through the CLI, deploy an image described by `.hostlier`, and receive a working HTTPS URL. Applications and routes survive CLI exit, controller restart, and server reboot.

### Installation and packaging

- Ship a downloadable Hostlier executable and an installer. Users do not need to install Node.js, Docker, or Caddy manually.
- Target Ubuntu 24.04 LTS on x86-64 and ARM64, with separate installation and reboot evidence required for each architecture. Add other operating systems only after the same installation tests pass on them.
- Package the CLI for Linux and macOS first. Test the controller on Linux; Windows CLI support can follow.
- After approval, `hostlier init` installs tested, pinned Docker and Caddy versions when absent, installs and starts the detached controller service, prepares persistent directories and execution logging, and configures required host firewall access.
- Detect existing components, occupied ports, and conflicting configuration before mutation. Reuse only compatible installations, preserving unrelated containers and routes. Stop with actionable diagnostics when safe integration is unsupported.
- Re-running installation converges without destroying data. Interrupted installation can resume. Infrastructure upgrades are explicit, with compatibility checks rather than silent major-version replacement.

### Initialization approval and network setup

`hostlier init` first performs read-only inspection and displays a concrete change summary. It identifies the selected runtime, software and versions to install or reuse, configuration and data paths, startup services, controller hostname, log location, and firewall changes including ports, protocols, and source scope. No server changes are made until the user approves. Declining or cancelling leaves the server unchanged. `--dry-run` shows the proposed changes without applying them; noninteractive execution requires explicit `--yes` authorization and still prints the summary. Changed conditions that require broader actions must produce a new plan rather than expanding the approved work silently.

Example summary for a fresh Docker installation:

```text
Hostlier will:
  Install the tested Docker and Caddy releases
  Create /etc/hostlier and /var/lib/hostlier
  Configure HTTPS at hostlier.apps.example.com
  Allow inbound TCP 80 and 443 through the supported host firewall
  Install and start hostlier.service, enabled at boot
  Write execution logs to /var/log/hostlier/hostlier.log with rotation

Apply these changes? [y/N]
```

The actual summary reflects the inspected server. Add narrowly scoped, persistent rules using a supported firewall backend; reuse equivalent existing access and record ownership of rules Hostlier creates. Preserve SSH/admin access and unrelated rules. Never flush the firewall or enable a new default-deny policy as an incidental part of opening web ports. If no host firewall blocks the required ports, report that no allow-rule change is needed. Unsupported firewall arrangements must produce actionable diagnostics instead of speculative edits.

For iteration 1, public inbound TCP 80 supports HTTP redirection and certificate validation; TCP 443 serves application HTTPS and the authenticated controller endpoint. Keep Docker, Caddy administration, database ports, and controller-internal listeners private. A Docker networking configuration must not inadvertently publish application or management ports around the host firewall rules. UDP 443 is not required by this initial contract.

Hostlier can modify the server's own firewall after approval. Provider security groups, upstream firewalls, and router/NAT rules require provider access that is outside iteration 1. Detect or report external reachability failures without claiming the endpoint is public merely because a local rule exists. Explain the remaining external change when it is needed. An occupied port is a service conflict, not a firewall problem; do not stop another service to free it without a separately reviewed action.

Persist installation progress and rule ownership so retries reconcile approved work without duplicating rules or overwriting unrelated configuration. Failure after partial setup reports completed and outstanding actions; it does not claim that all effects were rolled back. Re-running `init` presents the remaining changes for approval.

### Detached controller and execution logs

Run the controller as `hostlier.service`, supervised by systemd on the initially supported Linux platform. `init` starts it in the background, enables startup at boot, and waits for local readiness before reporting the controller running. It remains active after the invoking terminal closes or the CLI exits. Configure restart-on-failure with backoff, prevent duplicate controller instances from using the same state directory, and recover durable jobs after a restart.

Write controller execution logs to `/var/log/hostlier/hostlier.log`. Include timestamps, severity, operation IDs, application/resource identifiers, step transitions, outcomes, and sanitized error details. Record the initialization result and subsequent deployments, route changes, secret metadata changes, pairing/revocation events, and recovery actions. Do not record secret values, enrollment tokens, authorization headers, full request bodies, or unsanitized child-process command lines. Application stdout/stderr remains available through `hostlier logs <app>` separately from controller execution logs.

Use restricted file permissions and bounded log rotation/retention. Continue writing to the active `hostlier.log` after rotation and restart. Report logging failures through service diagnostics, with a stderr/journal fallback; an unavailable log file must not silently look like a successful logged operation. Durable job state remains the recovery source of truth rather than the text log.

```bash
# Local or authenticated remote controller inspection
hostlier controller status
hostlier controller logs --follow

# Direct inspection on the server
sudo tail -f /var/log/hostlier/hostlier.log
```

The controller log command can filter by operation ID, uses the authenticated controller API remotely, and never exposes logs to unpaired clients.

### CLI and controller

Proposed first-run flow after obtaining the executable:

```bash
# On the server
sudo hostlier init --domain apps.example.com
sudo hostlier pairing create --name my-laptop
# Displays the controller URL and a short-lived, single-use pairing token.

# On the laptop
hostlier connect https://hostlier.apps.example.com --name production
# Secure prompt: paste the token produced by the controller.
hostlier server use production

# In the application directory
hostlier validate
hostlier plan
hostlier deploy
hostlier status analytics
hostlier logs analytics --follow
```

### Controller and CLI pairing

Support both directions through explicit commands:

1. **Controller first:** an authorized operator runs `hostlier pairing create` locally on the server, or through an already paired administrator. The controller generates a cryptographically random, single-use enrollment token, with a proposed default expiry of ten minutes. The new CLI runs `hostlier connect <controller-url>` and supplies it through a hidden prompt or `--token-stdin`. A successful exchange creates a distinct client identity and credential and saves a named server profile.
2. **CLI first:** `hostlier connect <controller-url> --name production --request` creates a pending enrollment request and displays an approval code. An authorized server operator runs `hostlier pairing approve` and pastes that code at the prompt, reviews the requested client identity and permissions, and approves it. The waiting CLI completes enrollment automatically. The code alone cannot authenticate API requests or retrieve credentials; completion requires the separate request secret retained by the initiating CLI.

The first flow is the default onboarding path. Both issue per-client credentials rather than sharing a permanent server-wide token. Iteration 1 credentials represent trusted server administrators; the approval screen must state that authority. Fine-grained roles can follow later.

Enrollment tokens and requests expire, can be cancelled, and are consumed atomically. Invalid, reused, expired, or denied enrollments fail without creating a usable client. Bound and rate-limit unauthenticated enrollment requests and polling. Use standard TLS verification; do not provide an onboarding shortcut that disables certificate checks. Tokens and request secrets must not appear in URLs, command arguments, ordinary logs, or project configuration. Display a controller-issued enrollment token only in the explicit creation response.

Store client credentials in an OS credential store where supported, with a clearly documented owner-only file fallback. Store only non-recoverable token verifiers on the controller. Persist enrollment consumption and client revocation across restarts. Provide client listing and revocation, plus a local `disconnect` command that removes saved credentials and profiles. Distinguish local disconnection from server-side revocation: removing a local profile alone does not invalidate a copied credential.

Revocation blocks new requests and closes that client's authenticated streams. Already accepted durable deployment jobs continue under the server's recorded intent; revoking a client does not implicitly cancel or roll back those jobs. Local server administration can revoke clients and initiate replacement pairing without relying on a working remote credential.

The CLI is a required iteration 1 deliverable, including:

| Commands | Required behavior |
| --- | --- |
| `init`, `doctor` | Preview and approve server setup, including required host firewall rules; diagnose runtime, DNS, ports, certificates, and controller readiness |
| `controller status/logs` | Inspect the detached controller service and read or follow sanitized execution logs |
| `connect`, `disconnect`, `server list/use` | Pair with a controller over HTTPS, manage local connection profiles, and select a server |
| `pairing create/list/approve/cancel` | Issue enrollment tokens or approve CLI-initiated requests through authorized administration |
| `client list/revoke` | Inspect enrolled clients and revoke a client's controller access |
| `validate`, `plan` | Validate configuration and show intended resources, routes, changes, and limitations without provisioning |
| `deploy`, `status`, `list`, `logs` | Review and approve each deployment before submitting a durable job; report progress, expose useful diagnostics, and stream application logs |
| `stop`, `start`, `restart`, `rollback` | Manage application compute while preserving persistent data |
| `delete` | Remove application compute and routes; retain data by default, with an explicit data-purge option |
| `domain add/list/remove` | Manage domain entries and their active-route constraints |
| `secret set/list/delete` | Upload secrets through a hidden prompt, stdin, or a named local environment variable over the authenticated controller connection; list metadata without revealing values |

Support `--server` for explicit targeting, `--file` for project selection, nonzero failure exit codes, and `--json` for automation on data-returning commands. After explicit deployment approval, `deploy` waits for readiness by default; a detached option returns the operation ID. Detaching changes how the CLI waits, not whether approval is required. Retrying a request must not duplicate the application or volumes.

### Approval for every deployment

`hostlier deploy` resolves the project and CLI overrides, validates the request, and presents the concrete deployment plan before making workload changes. Running `hostlier plan` beforehand is optional and never counts as approval. The deployment command always shows the effective plan and waits for an affirmative response, with a default of no.

The review includes the target server and runtime, application, resolved image digest, URL and route changes, ports/network exposure, CPU and memory settings, volumes and retention behavior, secret names/revisions without values, dependency resources, and the update strategy with any expected interruption. In iteration 2, the same review covers PostgreSQL, Redis, and Neo4j provisioning and reuse. Standalone `resource create` also requires review and approval before provisioning.

```text
Deployment plan
  Server: production
  Application: analytics
  Runtime: Docker
  Image: ghcr.io/acme/analytics:1.0 (resolved digest shown here)
  URL: https://analytics.example.com
  Create: application container, private network, uploads volume
  Bind secret: analytics-api-key (revision shown here)
  Configure: Caddy route and automatic HTTPS

Apply this deployment? [y/N]
```

Declining, cancelling, EOF, or a disconnected CLI before approval causes no provisioning. Planning may inspect metadata and resolve an image digest, but must not pull workload images into the runtime, create containers or volumes, reserve active routes, issue certificates, or create dependency resources before approval. A no-change plan is reported as such and does not enqueue a deployment job.

Approval is specific to the displayed plan, server, authenticated client, and expected resource generations. The controller accepts execution only through an authenticated approval/apply flow tied to that plan identifier and fingerprint. It rejects missing, expired, altered, or stale plans. If configuration, image resolution, secret revisions, ownership, or relevant server state changes before apply, the CLI presents a new plan and requests approval again; it must not silently apply expanded or different changes. Persist approval metadata and the operation ID for audit and recovery, without storing secret values.

Each explicitly requested redeployment, update, or rollback needs a new approval. Repeating the same apply request after a lost response returns the original operation rather than creating another deployment. Once an approved job has been accepted, it may finish after the CLI disconnects and recover from a controller restart within the same approved scope. Automatic certificate renewal and runtime restart of an existing approved workload do not create new deployment approvals. A user-requested retry that would change the plan requires another review.

For the first two iterations, deployment and resource provisioning commands require interactive CLI approval: no `--yes`, environment variable, `.hostlier` setting, saved client trust, or detached mode bypasses it. Noninteractive deployment fails before provisioning. The separately documented `init --yes` applies only to server initialization. Read-only commands remain usable noninteractively; an automation-specific deployment approval workflow is deferred.

### Secure secret upload

Secret management is part of iteration 1. A paired CLI can add or update a named secret on the selected server using one of three mutually exclusive input modes:

```bash
# Hidden interactive prompt: typed or pasted input is not echoed.
hostlier secret set analytics-api-key --server production

# Read from stdin without echoing the value.
hostlier secret set analytics-api-key --server production --stdin < /secure/path/api-key

# Read the value of an environment variable already populated locally.
hostlier secret set analytics-api-key --server production --from-env ANALYTICS_API_KEY
```

The environment option takes the variable's name, never a shell-expanded value. Documentation must not suggest putting a literal secret in a command, `echo` argument, or shell assignment recorded in history. Hostlier does not accept positional secret values or a `--value` option. Without a terminal, the caller must choose `--stdin` or `--from-env`; missing input fails instead of hanging for a prompt. Missing environment variables and empty values fail clearly. Stdin is read to EOF, preserves newlines without silent trimming, and supports multiline secrets within a documented payload limit.

The CLI uploads the value in the body of an authenticated HTTPS request with normal certificate verification. It never puts secret values in URLs, arguments, success messages, error messages, or debug output. A local server invocation may use the permission-protected controller socket. Do not forward the local environment wholesale or pass uploaded secrets into unrelated child processes. Input modes protect against command-history and terminal echo exposure; they do not make the originating environment or runtime inaccessible to privileged local users.

Store values encrypted at rest in the server's secret store, with the encryption key protected separately from ordinary metadata and database exports. Acknowledgement means the write is durably committed. Retries after a lost response must not create unintended extra revisions. Normal list/status/JSON responses expose only names, revision identifiers, and usage metadata. There is no plaintext read-back command in iteration 1. Controller access logs and audit events record the operation and secret name, not the request body.

Applications reference uploaded names in `.hostlier`, as in `secrets: { API_KEY: secret://analytics-api-key }`. Planning and deployment validate that references exist. Updating a secret creates a new revision for the next explicit deployment; it does not silently restart running applications. Deployment records retain revision references for reproducibility and rollback, while resolved values stay out of general operation records. Reject deletion of revisions still required by active deployments or retained rollback revisions until those references are explicitly released.

### Deployment and routing

- Pull public and private-registry images using protected registry credentials. Record the resolved image digest for reproducible deployments and rollbacks.
- Create an application network, container, environment bindings, named persistent volumes, and supported CPU/memory limits.
- Check application readiness before exposing a new deployment. Report image pull, application health, route setup, and certificate readiness separately.
- Support multiple configured base domains, explicit custom hostnames, generated subdomains, and opt-in path routes.
- Enforce hostname/path ownership, including concurrent requests and overlapping path policies. Match `/app` and `/app/…` without capturing `/apple`.
- Preserve the previous healthy deployment when an update fails before activation. Stateless applications can switch to a healthy replacement; stateful applications use a documented stop/start strategy where concurrent access to a volume is unsafe. Do not promise zero downtime for every application.
- Rollback restores a recorded application revision. It does not reverse database migrations or mutations to persistent files.
- Preserve application data on stop, restart, ordinary update, and default deletion. Permanent data removal is explicit and scoped to owned resources.

### Completion gate

Release completion requires all iteration 1 acceptance tests below against a real fresh VM and the packaged CLI, including a controlled real-domain test of a publicly trusted HTTPS URL. The current local-only verification scope leaves this public-domain gate unrun. Installation and reboot behavior cannot be certified using mocked services alone.

## Iteration 2

### Outcome

Keep Docker as the default. Add optional k3s installation and first-party provisioning of PostgreSQL, Redis, and Neo4j instances. The CLI and `.hostlier` model remain consistent across runtimes.

PostgreSQL is the only SQL database in this iteration. MySQL and Microsoft SQL Server are deferred. Custom products remain deferred to iteration 3; these are built-in resource types with bounded schemas and lifecycle behavior.

### Optional k3s backend

```bash
sudo hostlier init --runtime k3s --domain apps.example.com
```

- Install a tested k3s version with its bundled containerd. Do not install Docker solely for a k3s server.
- Start with one node. Keep Caddy as the public edge, disable bundled Traefik, and implement private routing to application Services without competing listeners on ports 80/443.
- Include k3s installation, services, and necessary internal networking/firewall adjustments in the `init` approval summary. Keep the Kubernetes API and node/control-plane ports restricted to required local/private access; do not expose them publicly as a side effect of enabling application HTTPS.
- Translate workloads into Kubernetes Deployments or StatefulSets as appropriate, Services, secret references, resource constraints, and persistent-volume claims.
- Support the same application and route ownership model used by Docker.
- Persist the chosen runtime in installation state and reject attempts to change an established server's runtime by re-running `init` with a different runtime. Migration is a separate future workflow.
- Report local storage limitations accurately. A single-node cluster and local volumes do not provide server-failure redundancy or automatic storage failover.

### Built-in service instances

Provide both standalone resource commands and resources declared by an application:

```bash
hostlier resource create postgres app-db --storage 10Gi
hostlier resource create redis app-cache --storage 1Gi
hostlier resource create neo4j app-graph --storage 10Gi
hostlier resource list
hostlier resource status app-db
```

Proposed application configuration extension:

```yaml
dependencies:
  database:
    type: postgres
    source: provision
    storage: 10Gi
  cache:
    type: redis
    source: provision
    storage: 1Gi
  graph:
    type: neo4j
    source: provision
    storage: 10Gi

bindings:
  DATABASE_URL: database.url
  REDIS_URL: cache.url
  NEO4J_URI: graph.uri
  NEO4J_USERNAME: graph.username
  NEO4J_PASSWORD: graph.password
```

- Provision dedicated instances on the selected runtime using tested engine versions and explicit persistence settings. PostgreSQL is implemented and verified first, followed by Redis and Neo4j.
- Generate credentials, keep endpoints private, and inject service-specific connection fields into authorized application workloads. Do not expose database ports publicly by default.
- Validate each dependency with a real authenticated protocol operation before starting dependent applications.
- Support references to existing Hostlier-managed instances. Track dependents so stopping or deleting a shared resource cannot silently break other applications.
- Preserve provisioned data and credentials across application redeploys, controller restarts, and service restarts. Redis's selected persistence policy must be explicit and tested.
- Provide resource status, logs, start/stop/restart, and deletion behavior. Retain data by default; purge is explicit and disallowed while active dependents remain.
- Support logical backup and restore for the three built-in engines through supported engine tooling, with an independently restored instance proving recoverability. Backups must be exportable off-server; a local volume is not a backup.
- Defer automated major-version database upgrades. Pin engine versions and show required maintenance actions instead of silently replacing them.

### Completion gate

Pass application parity tests on Docker and k3s, plus authenticated data write/read, persistence, dependency binding, failure recovery, and backup/restore tests for PostgreSQL, Redis, and Neo4j on each runtime. Release PostgreSQL support first within the iteration; the iteration is complete only when the declared three-engine scope passes.

## Iteration 3

### Outcome

Add manual vertical scaling, application replica scaling, optional Nginx routing, and local Hosting SDK custom products. Third-party service integration is outside iteration 3: defer Aiven/Railway expansion, cloud VM provisioning and Fused Engine integration. Existing adapters remain available at their documented maturity; their completion is not an iteration 3 gate. Scaling is part of the approved deployment model, with durable desired state and runtime-specific capability checks. The CLI, runtime and recovery features below are implemented; [VALIDATION.md](VALIDATION.md) records the local E2E evidence and remaining acceptance limits. Unchecked acceptance items are not implied complete by implementation.

App CPU/memory limits already work through `.hostlier` and approved redeployment. Iteration 3 adds the dedicated scale command, replica management and replacement orchestration; it does not introduce app limits for the first time.

### Scaling tasks

- [x] Implement `hostlier scale APP --cpu … --memory … --replicas …` with plan review, approval and automatic progress reconnection.
- [x] Support replacement-instance scaling: provision a larger instance, verify readiness, switch traffic, drain the smaller instance, then stop it. Retain the smaller instance and its data for recovery; deletion requires a separate explicit action.

### Vertical scaling and replica count

- **Vertical scaling:** increase or decrease the CPU and memory allocated to an application service. Values apply per replica. This changes workload resources on the existing server; it does not resize the server itself or resize persistent storage.
- **Replica scaling:** increase or decrease the desired number of application instances. On k3s, Hostlier manages the Deployment replica count and routes through the existing private Service. Keep Docker as the simple default: initially support one application container or scale-to-zero, and reject counts above one before mutation with a clear k3s requirement.
- Support explicit scale-to-zero. Keep the application's route reservation, configuration, secrets and retained data; return a clear unavailable response while no replicas run. Scaling back up requires approval. There is no automatic wake-on-request in this scope.
- Start with manual scaling. Automatic scaling, adding cluster nodes, resizing a VM and multi-node high availability remain separate future work; application replacement on available capacity is covered below.

CLI:

```bash
hostlier scale analytics --cpu 2 --memory 2Gi
hostlier scale analytics --replicas 3
hostlier scale analytics --cpu 1 --memory 1Gi --replicas 2
hostlier scale analytics --replicas 0
```

Omitted flags preserve the service's current desired values. Each command shows the current and proposed per-replica limits, replica count, aggregate requested capacity, runtime, route, storage constraints and expected rollout/restart impact, then requires interactive approval. The same approval rules apply to scaling up, scaling down and scaling to zero; there is no `--yes` bypass. `--detach` returns only after approved durable acceptance.

`.hostlier` configuration:

```yaml
version: 1
name: analytics
image: ghcr.io/acme/analytics:1.0
port: 3000
route: analytics.example.com
replicas: 3
resources:
  cpu: "2"
  memory: 2Gi
```

`resources` continues to specify CPU/memory per replica; `replicas` defaults to one when omitted. Deployment compares the file's desired values with server state and includes differences in the approval plan. A CLI scale command updates server desired state without silently editing local files. A later deployment from an older file must clearly show any reversal of those values and obtain fresh approval.

### Scaling safety and lifecycle

- Bind scaling plans to the application revision, resource settings, replica count, client, fingerprint and server generation. Recheck ownership and capability before applying; serialize conflicting deployment, scaling and lifecycle operations for the same service.
- Retain the service URL and dependency credentials while scaling. New replicas receive the same declared secret revisions and authorized resource connections. Replica changes do not clone databases or provision a dependency per pod.
- For stateless services, admit new replicas to traffic only after readiness succeeds. On scale-down, drain and terminate selected replicas gracefully with a bounded timeout. Report rollout progress and desired, ready and available counts rather than claiming success at scheduling time.
- Vertical changes may require replacement/restart. Preview this impact and use the workload's safe update strategy; do not promise an in-place change or zero downtime. Validate CPU/memory quantities and scheduling capacity, surface unschedulable/OOM states, and keep the last healthy configuration recoverable when a change fails.
- Persistent application data requires an explicit concurrency/storage capability. Reject multiple writers to a local volume or other unsupported persistent-volume layout before mutation. Do not infer that a mounted volume or a database image is safe to replicate. Until an adapter explicitly supports a safe multi-replica storage model, volume-backed applications remain single-replica.
- `hostlier resource scale NAME --cpu CORES --memory SIZE` changes built-in database allocations through a separate approved maintenance plan. Docker and k3s PostgreSQL, Redis and Neo4j preserve data and credentials and restore old allocations on failed changes. Database replica counts require engine-specific replication, membership and failover support; copying PostgreSQL, Redis or Neo4j containers does not satisfy that contract and remains deferred.
- Persist desired scaling state through controller/server restarts. Scaling down never deletes persistent data or dependencies. Recovery, retry idempotency and rollback must preserve operation identity and require renewed approval for changed intent.

### Replacement-instance scaling

Keep the existing instance serving while provisioning its larger replacement. The approved plan identifies the source, target capacity, routing change, temporary overlapping capacity/cost, and any storage transfer or maintenance requirement. Switch traffic only after the replacement passes application and dependency readiness checks; confirm the route works before draining and stopping the smaller instance.

If provisioning or readiness fails, leave the smaller instance serving. If cutover fails, restore its route and keep it running. Persist each transition so a controller restart cannot prematurely stop the source or create duplicate replacements. Stateful workloads require an explicit safe data-transfer/replication strategy; reject unsupported moves during planning.

The initial target is a replacement application container/pod. Moving to a different server or provisioning a larger VM requires a capable host/provider adapter and a separate explicit destination in the plan; it is not implied by changing CPU or memory limits.

### Optional Nginx routing

- [x] Add Nginx as an optional routing backend; keep Caddy as the default.
- [x] Preserve the same domain/path routing, controller HTTPS, streaming/WebSocket and readiness behavior.
- [x] Include certificate issuance/renewal in the backend design so choosing Nginx does not require users to manage certificates manually. Validate configuration before reload; retain working routes on failure.
- [x] Test fresh installation, port conflict handling, reload failure, certificate renewal, replacement cutover and reboot locally. Actual HTTP-01 issuance and Certbot renewal use a trusted local ACME fixture; public ACME is a separate release check. Migrating an existing Caddy installation requires a separately reviewed migration plan; do not silently replace its proxy.

### Local custom products

Iteration 3 integrates Hosting SDK custom products with Hostlier's local provisioning model. Product authors can declare dependencies, inputs, secrets, lifecycle rules and public outputs using installed local packages and existing database connections. External hosted services and provider APIs are deferred. Products compose ordinary application and package configuration. Runtime capability checks reject unsupported replica/storage layouts during planning; products support installed-package `beforeDeploy` and `afterSuccess` hooks with exact-operation recovery, plus durable controller-enforced scaling policies. The optional parent TypeScript SDK exporter supports a documented public-container/PostgreSQL subset; unsupported contracts fail explicitly. The Go controller does not require Node.js. See [local products](docs/local-products.md).

- [x] Strengthen the package SDK with versioned configuration/output schemas, capability discovery, plan/apply/inspect/reconcile hooks, stable resource identities and reusable contract tests. The current executable protocol is a foundation, not the finished lifecycle SDK.

### Agreed recovery and visibility tasks

- [x] Replace controller JSON state persistence with embedded SQLite, including a verified migration from existing state and transactional workflow/event updates. No separate database service should be required.
- [x] Add workflow inspection and explicit reconciliation for uncertain steps, so recovery does not require editing controller state or creating a new identity. Verify ownership before accepting completion or authorizing a retry.
- [x] Add controller backup/export and restore, covering encrypted state, its separately protected key, configuration and package inventory; verify recovery on a fresh local server with separate application-volume and native database restoration, then repeat verification after reboot.
- [x] Add a concise application resource/status view showing configured limits, observed usage and readiness to help users choose scaling values. Keep this independent of a hosted monitoring service.

### SQLite state and recovery

SQLite becomes the controller's durable state store for applications, approved plans, operations, workflow steps, resource ownership, secret references and event cursors. Commit a step's execution intent before external work, and commit its result and event together afterward. Keep runtime calls outside database transactions. On restart, inspect the actual owned resource before reconciling an interrupted step; SQLite cannot atomically commit a Docker/k3s/database action with controller state.

Keep secret values and private workflow payloads encrypted with the separately protected key. Migrate existing `state.json` without changing controller ownership, credentials, operation IDs or event cursors; retain the original until migration is verified. Adapter-owned metadata also needs an explicit recovery plan rather than silently treating SQLite as its replacement.

Create consistent controller snapshots using a [SQLite-supported backup mechanism](https://www.sqlite.org/backup.html), not a naive copy of a live database file. Recovery bundles include configuration, protected key material, package versions/digests and ownership metadata. Database contents and application volumes require their own coordinated backups; controller metadata alone cannot restore them. A restored controller must not compete with the original for the same resources.

Acceptance tests cover interrupted migration, crashes around each workflow transition, uncertain external outcomes, event-cursor continuity, missing/wrong encryption keys, and restoration onto a fresh local server with independently verified application data. Resource visibility reads actual runtime usage and distinguishes unavailable observations from zero usage.

### Iteration 3 completion gate

Run the packaged CLI and real Docker/k3s scaling acceptance cases below, including approval, readiness, routing, data retention, failure recovery and reboot persistence. Add local Nginx routing/certificate parity, custom-product integration, SQLite migration, workflow reconciliation, full recovery and resource-visibility gates. No third-party account or API credential is required to complete iteration 3.

## Later work

Third-party managed-service expansion beyond the current direct Aiven/Railway packages, cloud VM provisioning (creating or resizing a server through a hosting provider API), Fused Engine integration, multi-node operation, additional SQL engines, source builds, and runtime migration remain later work. Their order is not committed by this proposal. Tunneling ([Bridge tunnels](docs/guides/bridge-tunnels.md)) and the local dashboard ([`hostlier ui`](docs/observatory.md)) were originally listed here and have since shipped.

### Deferred infrastructure packages: Terraform, Pulumi and cloud providers

Cloud infrastructure provisioning belongs in optional packages built with the Hostlier SDK. Packages may wrap Terraform/Pulumi or call a provider API directly. Hostlier supplies approval, protected inputs, durable operation tracking, output bindings and recovery coordination; the selected infrastructure tool owns its resource graph, provider state and locking. SQLite stores Hostlier workflow records and references, not a duplicate implementation of Terraform or Pulumi state.

- [ ] Add a Terraform package that generates a saved plan for review and applies that exact approved artifact. Pin module/provider/tool versions and protect plan/state artifacts and credentials. A changed or stale plan requires fresh approval. See [Terraform plan](https://developer.hashicorp.com/terraform/cli/commands/plan) and [saved-plan apply](https://developer.hashicorp.com/terraform/cli/commands/apply).
- [ ] Add a Pulumi package using its [Go Automation API](https://www.pulumi.com/docs/iac/concepts/automation-api/) for stack preview, updates and outputs. Package installation manages its required toolchain. Bind approval to the reviewed program/configuration and state; verify supported update-plan guarantees rather than assuming a preview locks the later update.
- [ ] Reuse resulting addresses, resource IDs and private connection outputs through workflow inputs. Provisioning a VM is followed by explicit server bootstrap/enrollment and readiness before deployment or traffic cutover; an IP address alone is not a ready Hostlier target.
- [ ] Test state locking, drift, interrupted/uncertain updates, output secrecy and recovery before enabling cloud mutations. Stop/delete remain explicit lifecycle actions, never automatic cleanup of an uncertain provisioning attempt.

Implement the SDK foundation alongside local package/custom-product work; defer actual Terraform, Pulumi and cloud-provider integrations beyond iteration 3. Prefer existing infrastructure providers for broad cloud coverage instead of rebuilding their resource lifecycle inside Hostlier.

### Deferred extension: connect a user's Fused Engine

After the current direct-provider and native-package work, add an optional Fused adapter for users who want to connect their existing Fused Engine. This is a later extension, outside iterations 1–3 and not a prerequisite for direct provider provisioning.

The adapter should let users compose Hostlier workflows from API operations and workflows they already use through Fused. Keep it in an optional package executable; the controller remains responsible for approval, secrets, durable operation state and deployment. Engine credentials use protected secret references. Each step selects its intended operation explicitly and maps private inputs and declared outputs through the existing workflow graph.

Before implementation, verify the Engine's supported execution and version-selection contract. Planning must disclose intended external effects without executing them. Applying invokes only the approved selection, keeps administrative outputs out of application bindings, and reconciles uncertain outcomes before any retry. The CLI continues to reconnect automatically while observing the Hostlier operation.

Acceptance will cover a user-supplied Engine connection, reuse of an existing API operation, dependent workflow steps, protected credentials/output visibility, approval rejection without side effects, and lost-response recovery without duplicate mutations. Use local contract tests plus a disposable connected Engine integration test before declaring support. Existing database-specific Usefused prototypes are reference work, not completion of this feature.

## End-to-end test strategy

Run tests through the packaged CLI, HTTPS pairing, and authenticated controller transport, with local-socket tests for initial setup and recovery. Inspect actual HTTP responses, runtime resources, files, and engine protocol results as independent evidence. A successful command exit alone is insufficient.

Use disposable Linux VMs for installer, systemd, firewall/port-conflict, and reboot cases. Use dedicated test domains and existing wildcard DNS pointed at those VMs for public routing tests. Tests must not modify production domains or resources.

For the current requested local-only run, use a local test certificate authority and disposable local services/VMs. Public ACME staging and production-issuer tests are deferred. The release strategy calls for a local test certificate authority for frequent deterministic integration tests and a public ACME staging service for validation/recovery tests. Staging certificates must never be reported as publicly trusted. Run a limited release-gate test against a public production issuer to verify browser-trusted HTTPS, avoiding unnecessary certificate requests.

Maintain small fixture applications: a version endpoint, an application with readiness failures, an upload/persistence application, a prefix-aware application, a streaming/WebSocket application, and clients that exercise each provisioned engine. Capture sanitized CLI output, controller events, image digests, resource inventories, and test assertions. Clean up only test-owned resources.

### Iteration 1 acceptance cases

| ID | Scenario and action | Required result |
| --- | --- | --- |
| I1-01 | Boot a clean supported VM with no Docker, Caddy, Node.js, or Hostlier; review and approve the packaged `init` command | Docker, Caddy, and the detached controller become healthy persistent services; required host web-port access is configured; the CLI can deploy without manual dependency installation |
| I1-02 | Re-run `init`; interrupt installation after a component is installed, then retry | Existing data/configuration survive; installation converges without duplicate services |
| I1-03 | Run setup with an unsupported OS, insufficient privileges, incompatible existing components, or occupied 80/443 | Preflight gives actionable failures; unrelated services/configuration remain unchanged |
| I1-04 | Create a controller-side enrollment token and connect from the packaged macOS/Linux CLI; reconnect using the saved profile | Token is exchanged for a distinct client credential; authenticated commands work without SSH; token cannot be reused |
| I1-05 | Deploy from `.hostlier`, deploy with `--file`, run with a missing/invalid file, and select two different servers | Correct project/server are targeted; invalid configuration causes no provisioning; JSON output and exit codes are usable |
| I1-06 | Run `plan` for create and update, comparing controller/runtime state before and after | Intended changes are accurate; no application, route, volume, certificate, or dependency is provisioned |
| I1-07 | Deploy a valid public image and an authenticated private image; try a bad tag and invalid registry credentials | Valid applications become healthy; failures are specific and sanitized; resolved digests are recorded |
| I1-08 | Deploy under two wildcard base domains and one explicit custom hostname | Each HTTPS URL serves the intended application; no cross-routing; unknown hostnames do not expose another app |
| I1-09 | Confirm an interactively suggested full subdomain address, redeploy that address through `route` or `--route`, then concurrently claim it from another app | Recorded allocation remains stable; only one owner wins; another application's route is never overwritten |
| I1-10 | Deploy prefix-aware applications at `/app` and `/reports`, with prefix preservation and stripping; request `/apple` | Correct app receives the intended path; redirects/assets work for the fixtures; unrelated paths are not captured |
| I1-11 | Use missing/wrong DNS or blocked certificate validation, then correct the prerequisite and retry | Deployment remains explicitly pending/failed until ready; retries recover without duplicate apps or routes |
| I1-12 | Check real public HTTPS, test certificate renewal with a controlled CA, and restart Caddy | Hostname verification succeeds for production certificates; renewal and restart preserve routing and certificate state |
| I1-13 | Deploy HTTP streaming and WebSocket fixtures through Caddy | Connections and responses work through the public route without unintended buffering or upgrade failures |
| I1-14 | Write application data; stop/start, restart, update, and reboot the VM | Data survives; running applications and routes recover; explicitly stopped applications remain stopped |
| I1-15 | Update v1 to healthy v2, try unhealthy v3, then roll back | Healthy update activates; unhealthy replacement does not displace the previous healthy app; rollback serves the recorded revision |
| I1-16 | Update a volume-writing app that cannot run two writers concurrently | Chosen strategy prevents concurrent writers; documented interruption and data preservation match actual behavior |
| I1-17 | Approve a deployment, disconnect the CLI after job acceptance, terminate/restart the controller, and retry the same apply request | The accepted operation recovers within its approved scope or reaches an actionable state; retry returns the original operation; no duplicate containers/volumes/routes are created |
| I1-18 | Inject unique test secrets and trigger errors; inspect CLI JSON, logs, plans, state exports, and process arguments | Values appear only in intended protected delivery/storage locations; diagnostics do not expose credentials |
| I1-19 | Remove a domain with active routes; delete an app normally; separately request explicit data purge | Active-domain removal is blocked; normal deletion retains data; purge removes only the selected owned data |
| I1-20 | Generate load against configured CPU/memory limits and inspect volume capacity reporting | Supported limits are applied; memory failures are visible; ordinary volumes are not claimed to enforce requested size |
| I1-21 | Inspect public listeners and call the controller API without credentials; try claiming the controller hostname for an app | Application HTTPS and the intended controller HTTPS endpoint are reachable; unauthorized management calls fail; bounded enrollment is the explicit exception; internal sockets/admin interfaces stay private and the controller route cannot be claimed |
| I1-22 | Upgrade Hostlier between two compatible packaged builds with applications running | State remains readable, routes/data survive, and the upgraded CLI/controller compatibility policy is enforced |
| I1-23 | Start CLI-requested pairing; approve it locally, then try completing the request from another client with only its approval code | Only the initiating CLI receives its credential after approval; pending requests cannot run management operations; approval codes cannot be used as credentials |
| I1-24 | Exercise invalid, expired, cancelled, and concurrently replayed tokens/requests; restart the controller between attempts; flood pending requests | At most one successful redemption occurs; expiry/consumption survive restart; requests are bounded and rate-limited; no unintended client is created |
| I1-25 | Pair two clients; revoke one during log streaming; disconnect the other locally; recover using server-local administration | Revoked access and streams terminate while the other client remains valid; accepted jobs continue; local disconnect removes saved credentials; local recovery can enroll a replacement |
| I1-26 | Pair against an invalid/mismatched HTTPS certificate; inspect logs, URLs, process arguments, profile files, and controller state after successful pairing | TLS errors fail closed; tokens appear only in explicit issuance or protected credential storage/delivery; server stores verifiers; ordinary profile metadata contains no secret values |
| I1-27 | Upload distinct secrets through a hidden prompt, stdin, and a named environment variable; bind them to a fixture application | Application verifies the exact values without printing them; interactive input is not echoed; CLI arguments, history, output, and controller access logs contain no values |
| I1-28 | Upload a multiline secret; try conflicting input modes, missing variables, empty input, oversized input, and a noninteractive call without an input mode | Supported content is preserved exactly; invalid requests fail clearly without changing stored values or hanging for a prompt |
| I1-29 | Attempt secret upload with a revoked credential or invalid TLS certificate; inspect server metadata and encrypted storage | Unauthorized uploads fail; plaintext is absent from general records and storage exports; successful responses never return the value |
| I1-30 | Lose an upload response and retry; restart the controller; rotate a secret, redeploy, roll back, and attempt deletion while referenced | Durable writes survive restart; retries do not create extra revisions; running apps change only on explicit deploy; rollback uses the recorded revision; referenced secrets cannot be deleted |
| I1-31 | Use a domains-only `config.yaml`; deploy root, subdomain, path, and subdomain-plus-path addresses using scalar `route` and `--route`; override a file-defined subdomain with a CLI path address | No separate domain/subdomain/path fields are required; CLI overrides replace the full address; plan and deploy agree on the effective URL; the file stays unchanged |
| I1-32 | Omit routing in interactive and noninteractive runs; submit invalid addresses, unsupported schemes, embedded credentials, ports, queries, fragments, unsupported domains, or deceptive domain suffixes directly to the API | Interactive mode collects a deliberate address; noninteractive and invalid requests fail before provisioning; equivalent addresses share ownership checks; controller enforces domain membership at DNS label boundaries |
| I1-33 | Run `init --dry-run`, decline an interactive plan, and invoke noninteractively without `--yes`; then approve the same plan | Inspection-only paths leave packages, services, persistent files, and firewall rules unchanged; approved setup applies only disclosed actions; explicit noninteractive approval is required |
| I1-34 | Initialize with a supported firewall blocking web traffic, existing SSH access and unrelated rules; retry setup and reboot; separately simulate an upstream firewall block | Approved web rules persist without duplication; SSH and unrelated rules survive; only intended public ports are exposed; upstream blocking is reported without a false readiness claim |
| I1-35 | Initialize, close the terminal, run a remote deployment, crash the controller, and reboot; attempt a second controller on the same state directory | Service stays detached, restarts with backoff, recovers jobs, and starts at boot; duplicate ownership is rejected; application routing remains available independently of controller availability |
| I1-36 | Inspect `hostlier.log` and authenticated `controller logs` during success/failure/recovery; rotate logs, restart, and make the log directory unwritable | Execution records are correlated and sanitized; permissions and retention are enforced; logging resumes into the active file; write failures are visible through fallback diagnostics; unauthenticated log access fails |
| I1-37 | Plan and deploy a new app, redeploy, update, and roll back; decline or interrupt each prompt before finally approving | Every requested deployment displays its effective plan; previous approvals do not carry over; no workload/route/certificate/volume changes occur until the current plan is approved |
| I1-38 | Submit apply without approval, change the plan or resource generations before apply, and replay an accepted apply after a lost response | Controller rejects missing/stale/altered approval; CLI requires renewed review of changed intent; replay returns the original accepted operation |
| I1-39 | Attempt deployment without a terminal, with `--yes`, with a configuration/environment bypass, and with detached mode | No bypass skips the approval prompt; noninteractive deployment fails before provisioning; detached jobs begin only after interactive approval |

### Iteration 2 acceptance cases

| ID | Scenario and action | Required result |
| --- | --- | --- |
| I2-01 | Initialize a fresh VM with `--runtime k3s`; reboot and re-run setup | k3s/containerd, Caddy, and controller recover; Docker is not required; no Traefik/Caddy port conflict |
| I2-02 | Run deployment, routing, secret, persistence, update, deletion, and recovery fixtures against Docker and k3s | Both backends honor the same user-visible contract or reject unsupported fields before mutation |
| I2-03 | Provision PostgreSQL, create a test table and row through an application, then query it | Authenticated writes/reads succeed; only authorized applications receive connection credentials |
| I2-04 | Provision Redis, write keys, restart the service and reboot according to its declared persistence policy | Authentication works and persistence matches the documented policy; no unadvertised data durability claim |
| I2-05 | Provision Neo4j, create nodes/relationships, restart, and query them | Authenticated graph operations work and persistent data survives |
| I2-06 | On each runtime, deploy an application with all three declared dependencies and connection bindings | Dependencies become protocol-ready before the app; app can exercise all connections; secrets are redacted |
| I2-07 | Attempt unauthenticated access, inspect external ports, and test access from an unrelated app network/namespace | Databases are not publicly exposed; unauthorized access fails; configured network separation is enforced |
| I2-08 | Reference one resource from two applications; attempt resource deletion while in use | Both apps use the intended instance; dependency tracking blocks accidental destructive deletion |
| I2-09 | Redeploy/delete an application, restart the controller, and separately stop/start each resource | Declared retention holds; data and credentials remain stable; explicit resource operations affect only their target |
| I2-10 | Interrupt resource provisioning after storage or credentials exist; retry after controller restart | Provisioning reconciles existing owned resources without creating duplicate instances or rotating credentials unexpectedly |
| I2-11 | Trigger failed image pulls, authentication failures, readiness failures, and exhausted storage | Clear resource-specific failure states appear; dependent app activation is blocked; retained data is not silently deleted |
| I2-12 | Back up each engine, export the backup, restore to a separate instance, and query original test data | Restore is independently verified for PostgreSQL, Redis, and Neo4j on both runtimes; backups protect credentials/data |
| I2-13 | Re-run `init` with a different runtime on an initialized server or request an unimplemented engine/provider/major-version upgrade | Validation rejects the unsupported transition before altering workloads or persistent data |
| I2-14 | Check resource constraints and storage capability reports on each runtime | Applied limits match the plan; requests and enforced quotas are distinguished; no local-storage failover claim |
| I2-15 | Review and approve fresh k3s setup; inspect host/private networking and public ports after setup and reboot | Planned internal rules persist, cluster networking works, web traffic reaches Caddy, and Kubernetes API/node ports are not inadvertently public |
| I2-16 | Plan an app with PostgreSQL/Redis/Neo4j dependencies and separately invoke each standalone `resource create`; decline, then approve | CLI shows the full dependency and storage changes; no instances or volumes are provisioned before approval; approved operations match the reviewed plan on both runtimes |

### Iteration 3 scaling acceptance cases

| ID | Scenario and action | Required result |
| --- | --- | --- |
| I3-S01 | Increase and decrease application CPU/memory through the CLI and `.hostlier` on Docker and k3s | Approved per-replica limits are independently observed in the runtime; planned restart/rollout behavior matches execution; the URL remains stable |
| I3-S02 | Scale a stateless k3s service from one to three replicas and back to one | Desired/ready counts converge; responses identify healthy replicas behind the same URL; downscaling drains removed replicas within the documented timeout |
| I3-S03 | Scale to zero, reboot, then scale back up on both runtimes | Zero desired replicas remains durable; the route stays reserved with an unavailable response; configuration, secrets, dependency data and persistent application data are retained |
| I3-S04 | Decline or interrupt scaling approval; try missing TTY, `--yes`, stale plans, concurrent changes and repeated apply requests | No unapproved scaling occurs; stale intent requires new review; the original accepted operation is returned on retry; conflicting mutations cannot overwrite each other |
| I3-S05 | Scale via CLI, then deploy a file with older resource/count values | The plan exposes the reversal explicitly and requires fresh approval; local files were not silently changed by CLI scaling |
| I3-S06 | Request multiple replicas on Docker, a volume-backed application without a safe concurrency capability, or a built-in database without replication support | Planning rejects the unsupported operation without containers/pods, volumes or dependency mutations |
| I3-S07 | Trigger insufficient capacity, failed readiness and memory pressure during scaling | Actual pending/failure state is reported; unready replicas receive no traffic; existing healthy capacity is preserved where feasible; rollback/recovery is explicit and does not delete data |
| I3-S08 | Scale an app with shared database/cache dependencies and rotated secret revisions | All admitted replicas use the approved bindings/revisions; the resource instance and credentials are not duplicated; dependency reference protection still holds |
| I3-S09 | Disconnect the CLI or restart the controller during an accepted scale, then retry/recover; reboot after completion | Durable desired state and operation identity survive; retries do not create duplicate workloads; completed scale settings return after reboot |
| I3-S10 | Roll back a scaled revision and vertically resize a supported database resource | Rollback shows the resource/count changes for approval; engine maintenance behavior matches the resource plan; independently queried data survives; unsupported adapters fail before mutation |
| I3-S11 | Provision a larger replacement while continuously requesting the app URL | The source serves until replacement and route checks pass; traffic switches, existing requests drain, then the source stops; its data remains retained |
| I3-S12 | Fail replacement provisioning, readiness, or route cutover; restart the controller during each transition | The source remains available or its route is restored; no premature stop, duplicate replacement, or data deletion occurs |
| I3-S13 | Request replacement for a stateful app or a different server | The plan requires supported data transfer and destination capabilities; unsupported moves fail before mutation |

## Implementation order

1. Define versioned server/project schemas, CLI contracts, operation states, resource ownership, and adapter interfaces.
2. Build the packaged CLI/controller, protected local administration, controller HTTPS endpoint, both pairing flows, per-deployment approval, and durable authenticated command path.
3. Complete the approval-based Docker/Caddy installer, host firewall setup, detached controller service and execution logging, and a single real image-to-HTTPS deployment.
4. Add multi-domain and path routing, persistent volumes, secrets, registry authentication, and lifecycle/recovery behavior.
5. Run the iteration 1 matrix and resolve failures before expanding the runtime scope.
6. Add the k3s backend and run the application parity suite.
7. Add PostgreSQL provisioning and its full data/backup/recovery tests, then Redis and Neo4j using the same resource lifecycle model.
8. Run the full iteration 2 matrix before expanding into iteration 3.
9. Add the dedicated scale command, approved vertical scaling, replica management and replacement-instance scaling; run the iteration 3 scaling matrix and expose supported capabilities through local custom products.
10. Add optional Nginx routing and local Hosting SDK custom products with their own approval, ownership and local integration tests.
11. Revisit third-party service and Fused Engine integrations after iteration 3.

## Technical references

- [Docker Engine API](https://docs.docker.com/reference/api/engine/)
- [Caddy automatic HTTPS and certificate validation](https://caddyserver.com/docs/automatic-https/)
- [Caddy configuration API](https://caddyserver.com/docs/api)
- [Caddy host and path routing patterns](https://caddyserver.com/docs/caddyfile/patterns)
- [k3s runtime configuration](https://docs.k3s.io/advanced)
- [k3s networking and packaged Traefik](https://docs.k3s.io/networking/networking-services)
- [Existing Hosting SDK overview](../README.md)
- [Existing product and provisioning model](../docs/native-products.md)
