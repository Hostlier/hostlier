# Local deployment observatory

The dashboard runs inside the CLI on your computer. The controller and JetStream run on your server or test Linux VM. Only deployment observations and approved API requests cross that connection.

Start the local dashboard before or after pairing:

```sh
hostlier ui
# Or choose a controller already paired by the CLI:
hostlier ui --server production
# Optional fixed loopback port:
hostlier ui --server production --port 4177
```

Open the complete URL printed by the CLI. Its fragment contains a random local dashboard capability, not the controller credential. The browser retains this local capability in session storage so refreshing works. The listener binds only `127.0.0.1`; the controller credential stays in the CLI process. The UI can start without a profile. Its Controller panel selects saved profiles or requests administrator-approved pairing with a new HTTPS controller; root Unix-socket administration is never exposed to the browser. Ctrl-C closes the local server and its observation worker without canceling accepted controller operations.

For a new controller, enter its HTTPS origin and a profile name. The local CLI requests a pairing challenge and retains the private polling secret. Review the displayed code in a trusted server terminal with `hostlier pairing approve CODE --socket /run/hostlier/controller.sock`; inspect the requested device and confirm enrollment. The dashboard then saves the credential through the ordinary owner-only CLI profile store and starts its one upstream subscription. Email/device enrollment through `hostlier connect --email` continues to use the trusted terminal; its existing saved profiles can be selected in the dashboard.

Switching profiles cancels and joins the previous observer, clears its cached data, and saves the new CLI default. Pending action approvals prevent controller switching until their exact request has completed, keeping each retained approval bound to its original controller. TLS verification applies to enrollment and observation; `HOSTLIER_CA_FILE` can extend trust for a private VM certificate without disabling verification.

The dashboard shows application runtime state, replica readiness when available, measured CPU/memory, capture time, and a bounded operation inventory. Running means the runtime reports a running process; it is not an independent end-to-end availability probe. Unknown measurements appear as unavailable. Connection loss preserves the last observation with an explicit warning; capture age over 15 seconds marks the shared observation stale. Empty or failed observations are never presented as healthy deployments.

Lifecycle, scale, rollback, recovery, revocation and other changes are handed off as copyable CLI commands. Commands preserve the running CLI executable, saved-profile directory, selected controller, private-CA trust and enrolled-host selection where applicable. Values are shell quoted; unknown values use uppercase placeholders. The browser never runs a command, signs an approval or executes a lifecycle change. The CLI retains its existing plan/review and independent hardware approval workflow.

## UI components and embedded assets

The dashboard uses a black-and-white Tailwind interface with shared DOM components for buttons, status badges, metric cards, empty states, deployment rows and operation rows. Source files live in `ui/src`; `components.js` and `primitives.js` own reusable UI pieces, `domains.js` renders controller inventories, and `app.js` owns observation and connection state. Rows reconcile by identity, preserving keyboard focus during automatic status updates. Status is conveyed with text and shape rather than color alone.

After changing frontend source, regenerate the compiled assets from the `hostlier` directory:

```sh
make ui-build
make build
```

The frontend build uses pinned Tailwind CLI and esbuild dependencies from `ui/package-lock.json`. It writes `internal/localui/assets/index.html`, `style.css` and `app.js`. Keep these generated assets with the source changes. Go embeds all three in the executable; ordinary Go builds and running `hostlier ui` require neither Node.js nor a separate frontend server. Fonts, icons, JavaScript and styles do not depend on a CDN. The local HTTP server retains its same-origin, session and content-security checks.

## Capture and workers

Each controller owns one observation scheduler goroutine. Recent snapshot readers or live streams activate capture; after 15 seconds without interest it becomes idle. Active viewers share a five-second sampling cadence. A fixed pool of at most four goroutines probes applications for each capture, with a two-second per-application context and a ten-second total cycle context. Runtime adapters must honor cancellation. Workers finish before the snapshot is serialized and the next cycle begins, so slow probes cannot accumulate overlapping cycles.

Runtime calls execute outside the authoritative controller state lock. Capture briefly locks state to copy public scalar metadata; its own locks protect only demand and stream admission. Deployment execution continues through the existing durable operation workers. The observer does not acquire the deployment executor lock, persist measurements in SQLite, or depend on a browser connection. Completed snapshots pass through private embedded JetStream storage before becoming visible to viewers. Existing autoscaling and group-readiness monitors retain their own responsibilities.

The application and operation inventories are each limited to 128 entries per snapshot. A `truncated` marker tells clients to load the complete retained inventory on the deployment or operation page (or through ordinary CLI commands). Applications sort by name; active and interrupted operations sort ahead of terminal receipts. Each snapshot has a process epoch and sequence ID. Retained broker snapshots keep those identities across a restart, but the controller serves cold desired metadata until a capture from its new epoch arrives. Observation IDs are not durable operation cursors.

## Wire contract

Both endpoints require the ordinary currently live bearer credential or protected local authority:

- `GET /v1/status/snapshot`: return the newest completed cached observation immediately. A cold controller returns desired metadata with an empty observation timestamp and schedules the first capture.
- `GET /v1/status/events`: send SSE `snapshot` events containing complete replacements. A connection always begins with the latest replacement, so reconnecting needs no delta replay or `Last-Event-ID`. Observation identities reset with the controller process.

Connections rotate after 20 seconds, heartbeat every five seconds, and recheck authorization before snapshots and at heartbeats. Controller admission is limited to 32 status streams. Writes have a two-second deadline and occur outside controller and bus locks. Credential revocation ends delivery and a reconnect returns the ordinary unauthorized error.

The CLI opens one upstream stream for all its browser tabs. Each browser uses an authenticated local SSE endpoint. Local stream admission is also bounded to 32. The internal bus stores one immutable JSON replacement; each subscriber has one coalescing notification. A slow viewer skips superseded observations and reads the latest complete state instead of blocking producers or accumulating an unbounded queue. This provides current state, not historical telemetry or an audit log.

The public observation schema excludes deployment configuration, environment, secret references, encrypted records, free-form provider errors, probe output and workflow data. Usage includes numeric measurements, timestamps and fixed safe availability explanations. Existing operation-event streaming retains its durable log cursor and recovery contract independently.

## Read path changes

Ordinary successful GET/HEAD requests no longer save the controller aggregate. Mutations still require durable persistence. Responses are serialized while their mutable references are protected, then written after releasing the state lock. Application status/log inspection, doctor probes and resource status/log inspection release the state lock during backend I/O. Existing usage inspection already did this.

The existing operation-event stream can still save previously uncommitted execution events before releasing durable replay cursors. Removing that save requires a separate producer-durability change; telemetry observation snapshots never take that path.

## Embedded JetStream

Every controller starts a private NATS server with JetStream inside the controller process. It uses `DontListen` and in-process connections: there is no external NATS service, client port, monitoring endpoint, cluster listener or browser connection to NATS. The CLI continues to use the authenticated HTTPS snapshot/SSE API.

`internal/observation.Bus` separates capture from delivery. One background publisher queues completed public snapshots, waits for JetStream acknowledgements outside controller locks, and retries transient failures with capped backoff. Only one pending replacement is retained, so capture workers and normal controller operations do not wait for the broker. Retries use the snapshot identity for deduplication. A newer capture may supersede an unpublished replacement; this is a latest-state telemetry channel, not a command queue or complete event history.

One durable pull consumer serves all CLI subscriptions, with at most one unacknowledged message and one outstanding pull. It updates the immutable cache before confirming acknowledgement with JetStream. Stream sequence watermarks suppress older or repeated deliveries after acknowledgement loss or restart. Browser and CLI readers access that cache without broker I/O and keep their bounded coalescing notifications.

The file-backed `HOSTLIER_STATUS` stream uses only `hostlier.observation.status`. It retains at most 64 snapshots, 8 MiB, or two minutes, discarding oldest messages as limits are reached. Payloads reserve framing space within the CLI's 4 MiB event limit. JetStream's server storage budget is 16 MiB and its configured memory budget is 8 MiB; these budgets do not cap the total Go process memory. Storage lives in the owner-only `STATE_DIR/observations` directory. Synchronous file storage is enabled for broker acknowledgements.

Payloads are projected through the public observation DTO before publication, dropping unknown configuration or credential fields. SQLite remains authoritative for accepted commands, approvals, deployments and operations. Reconstructible JetStream files are excluded from controller recovery bundles. Recovered telemetry can warm the internal cache, but an older controller epoch is never presented as a fresh readiness capture. Start/stop/restart authority remains with the controller's existing approval flow.

A broker outage after startup leaves the last delivered sample and its original timestamp visible. The existing UI freshness rules mark it stale; pending captures continue to coalesce and publication retries run in their own worker. Closing the controller cancels capture and delivery, interrupts private client requests, waits for owned workers and flushes/shuts down the embedded server. Unpublished replacements may be discarded on shutdown. Broker initialization failures release SQLite ownership and return a startup error rather than claiming observation readiness.

## Verification

Real JetStream tests cover private startup, acknowledged delivery, deduplication, retention eviction, restart recovery, public DTO projection, outage coalescing, shutdown and recovery-bundle exclusion. Controller integration checks verify that older epochs stay cold. The observation, client and dashboard tests also cover blocked runtime isolation, four-worker admission, timeouts, sample sharing, bounded slow subscribers, TLS transport, reconnect replacements, revocation, read requests with unavailable persistence, same-origin/local session checks, and approval-only actions. Browser QA uses synthetic observations and does not exercise a real Docker/k3s deployment or physical hardware-key ceremony.


## Dashboard pages

The controller dashboard starts with an **Apps & tunnels** grid and four navigation items: Apps & tunnels, Logs, Activity and Settings. Cards identify each item as an App or Tunnel; tunnel details show bridge connection status separately from application runtime health. Open an app to enter its own dashboard. Its sidebar switches to **Overview, Metrics, Logs, Activity, Resources and Settings**, with an **All applications** link back to the controller. Direct links and browser history preserve the selected app and section.

App Overview shows runtime health and the current deployment. Metrics fixes the application filter to the selected app; Logs and Activity use a shared time-range viewer scoped to that app. Logs reads collected workload history and offers a separate **Current runtime output** disclosure for the existing Docker/k3s log endpoint with a component selector. Runtime output remains available while the runtime retains the pod or container logs. Activity reads persisted controller events, including operation progress and completed management requests. Resources links its dependencies to runtime inspection. App Settings contains endpoints, configuration, components, revisions and collapsed CLI management commands. Sections load independently instead of fetching logs, workflow steps and monitoring history on every app page.

Controller Settings provides links to shared resources, servers, recovery, access and controller-wide monitoring. Domains, secret metadata and controller diagnostics are available in expandable sections. Existing inventory and detail URLs continue to work.
Recovery shows backup policies, verified offsite points, native backup receipt metadata, jobs/upgrades and storage identities. Access shows device identity, revocation, human approval requirements and finite authorization windows. Settings shows domains, secret names/revisions and runtime diagnostic summaries. Each section distinguishes unavailable data from an empty inventory. Copyable CLI handoffs provide the relevant inspection or management commands.

The resource inventory reads `GET /v1/resources?observed=false`, which returns retained metadata without invoking a runtime probe for every row. Opening a detail page explicitly inspects one resource. Ordinary CLI resource inventories preserve their observed behavior. Authenticated `GET /v1/backups` returns receipt metadata; backup export and restoration retain their independent approval boundary.

Only the visible page refreshes: observations/logs use the existing five-second cadence; broader inventories refresh every 15 seconds. Browser tabs share a short controller/query cache and at most four cancellable read workers. Changing controllers invalidates page generations and cache namespaces. Dynamic CLI arguments are quoted and credential values never appear in commands or browser state.

Run frontend checks with `npm test --prefix ui` and rebuild embedded assets with `npm run build --prefix ui`.

Docker resource capture uses `stream=false&one-shot=true` so Docker does not wait for two sampling cycles within the shared two-second probe deadline. CPU consumption is calculated from consecutive cumulative counter samples for the same verified container identity. A separate observation mutex protects a bounded 256-container baseline cache, without holding the runtime mutation lock during daemon I/O. The first sample displays measured memory and a CPU warmup explanation; a subsequent idle CPU delta is measured zero. Counter resets, out-of-order reads and container replacements cannot reuse another baseline or fabricate zero usage. Raw provider diagnostics remain outside the shared status stream.


The opt-in VM audit runs with `HOSTLIER_UI_TOKEN=SESSION_FRAGMENT npm run audit:vm --prefix ui` while the CLI dashboard is running. Override `HOSTLIER_UI_URL` for a different loopback port. `HOSTLIER_UI_AUDIT_REPORT` optionally writes a private JSON summary. The audit renders every page with actual controller responses, follows inventory and relationship links, checks component logs and telemetry history, exercises command copying, validates all command arguments, and executes the displayed read-only CLI handoffs. Change templates are checked with `--help`; protected mutations are never submitted. Empty fleet/recovery inventories remain empty rather than creating test infrastructure on the controller.

The 2026-10-30 VM audit checked 43 pages/filter views, 43 links and 82 API reads. It validated 57 command forms and executed 32 read handoffs. Seventeen frontend tests and the full CLI/local-UI race suites passed. Missing Neo4j engine tooling was installed on the VM, restoring status and logs for the existing database without restarting its container. A genuine application access request was confirmed in the observability log history. The VM has no enrolled fleet peers or recovery jobs; those detail contracts have fixture coverage, while their real inventories were checked for correct empty states. Managed-collector supervision reports its own missing image assets separately from the already-running agent's successful data delivery.

Navigation shares one route catalog for labels, explanations, selected states and detail breadcrumbs. Controller settings links to Databases & caches, Servers, Backups & recovery, and Access & devices. Narrow screens use a labeled expandable menu. Selecting the current page preserves its controls and unapplied filters.

A normal status-stream rotation no longer inserts a disconnected banner or clears page data. The header allows a three-second reconnect grace while the last capture is still fresh, then reports Reconnecting and eventually Updates paused with the retained capture time. Sustained loss is visible without moving the page. Refresh failures retain successful inventories, charts, logs and command controls; failures under new filters cannot reuse another filter's records. Explicit controller resets still discard old observations. Expired local sessions require the fresh CLI URL. Verification includes 27 frontend tests, local-UI race tests, the VM page audit and a 45-second real stream check with three brief reconnect events and no change in its displayed Up to date state.

### Searchable Logs and Activity

Controller and app Logs/Activity default to the past two hours. Choose a preset or an explicit local date/time range, filter by literal message text and level, move backward or forward one hour, or expand the range up to 30 days. The viewer shows its timezone and stores filters in the URL. Older/newer controls shift the window by one hour; Load more continues within the same fixed window. New app or filter selections discard late responses from previous queries.

`GET /v1/activity` reads controller events from SQLite with `app`, `operationId`, `from`, `to`, `q`, `level`, `limit`, `before` and `beforeTime`. Snapshot commits archive new activity in the same transaction; a controller-owned worker flushes runtime progress between state transitions. History survives controller restart and the existing 1,000-event in-memory ring. Existing installations backfill only events still present in their snapshot; previously discarded events cannot be recovered.

Activity is retained for up to 30 days, 100,000 entries or 64 MiB of logical payload, whichever limit is reached first. Expired entries are excluded immediately; physical trimming accompanies new entries. The viewer reports the oldest retained entry. History reads require the existing administrator read authority, remain read-only, and do not grant deployment-scoped clients global history. Collected logs retain their separate telemetry storage and ownership checks described in [telemetry collectors](telemetry-collectors.md).
