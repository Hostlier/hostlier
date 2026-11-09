# Usefused database deployment contract

This is the versioned contract consumed by the implemented Aiven and Railway Go packages. It specifies the hosted app still to be connected once Engine access and exact provider operations are available. It is **not** an already deployed app or an executable provider implementation.

Both packages may share one `kind: unified_app`. Its provider routing is selected by `input.provider`. Keep provider credentials in selected Fused buckets or connected-user records and route through the configured `userRef`. Select exact Registry operations per service and immutable service versions; do not use `select_all`, guessed operation IDs, or provider tokens in code.

Publish an SDK or direct API consumer selecting the hosted app by exact name and version under alias `database_deploy`. The installer receives the immutable consumer version ID and a consumer execution token. Hostlier invokes:

```text
POST <engine-origin>/v1/apps/<consumer-version-id>/executions
Authorization: Bearer <consumer-execution-token>
```

```json
{
  "operation": "unified_app:database_deploy",
  "input": {
    "version": 1,
    "action": "plan",
    "provider": "aiven",
    "key": "controller-namespace/analytics/database",
    "userRef": "hostlier-production",
    "spec": {
      "engine": "postgres",
      "mode": "new-database",
      "database": "analytics"
    },
    "inputs": {"connection": "<private administrative connection>"}
  }
}
```

Inputs `previous` and `plan` are optional opaque JSON. `previous` is retained private provider state when inspecting a completed owned resource. `plan` is supplied only for approved apply and must be the exact read-only plan produced earlier. `inputs.connection` is present only when the workflow explicitly binds one; it is never the consumer execution token.

## Read-only planning

For `action: plan`, inspect provider identity, privileges, available engines, existing resources and target placement. No service, database, user, storage, public endpoint or deployment may be created during planning. Return a concrete review of costs, immutable image/template selections, persistent storage, network exposure, TLS, ownership conflicts and expected mutations. A change in provider state must invalidate the earlier plan at apply.

The authored app output must match:

```json
{
  "version": 1,
  "action": "plan",
  "provider": "aiven",
  "key": "controller-namespace/analytics/database",
  "ready": false,
  "summary": ["Create the reviewed PostgreSQL database and scoped application user"],
  "plan": {"opaqueReviewedProviderPlan": "..."}
}
```

The Engine wraps this in its normal `{ "status": "succeeded", "output": ... }` envelope. Plan output must not contain `result`. The opaque plan is stored encrypted by Hostlier; summaries must exclude credentials. An unavailable or unsupported engine/mode must fail here, before the app deployment is approved. Runtime instance creation needs a reviewed provider project/region/plan or Railway project/environment/template/volume specification. Do not infer capacity or exposure from a mutable template during apply.

## Approved apply

For `action: apply`, validate the returned plan against the full provider/spec/key/user identity and current provider state. Provision only the approved changes. Use the stable key for deduplication and ownership; never adopt an unrelated service or database just because its name matches. Persist provider identities and reconcile uncertain provider calls instead of repeating creates. Do not delete a shared parent instance.

Before claiming readiness, verify TLS, reachability from the intended Hostlier deployment, database existence, and the application's scoped credentials. Do not substitute a rewritten connection URL for actual creation. Aiven API database management and SQL/driver execution against Railway databases are different capabilities: inspect the available Registry operations and supported Engine execution model before promising either. Reject unsupported modes rather than using a generic SQL string or assuming Railway exposes a database-creation API.

Successful authored output:

```json
{
  "version": 1,
  "action": "apply",
  "provider": "aiven",
  "key": "controller-namespace/analytics/database",
  "ready": true,
  "result": {
    "state": {"providerId": "...", "ownershipKey": "controller-namespace/analytics/database"},
    "outputs": {"url": "<private scoped application connection>"}
  }
}
```

Provider errors, pending execution, mismatched identities and absent readiness all fail closed. The adapter does not retry HTTP calls automatically. Output and state are private and encrypted by Hostlier. Raw provider exceptions must remain in Fused private diagnostics rather than summaries.

## Remaining hosted-app work

CLI discovery returned `engine_unavailable`; no service/version/operation selectors were verified and no remote mutation was attempted. The configured Engine's `/health` endpoint subsequently returned HTTP 200 with `unified_app_worker_ready: true`, while another request failed to resolve `eng.usefused.com`. Current evidence points to intermittent DNS/connectivity in the execution environment, not a confirmed Engine outage. Once discovery works:

1. Discover enabled Aiven and Railway services and inspect exact operation input/output/auth contracts.
2. Confirm database creation capabilities separately from managed instance provisioning. Use provider API operations where supported; identify a supported secure database executor for direct-connection modes. Reject unsupported combinations explicitly.
3. Author the default-exported `buildUnifiedApp` source under `.fused/unified_app/`, selecting only verified operations and credentials through existing buckets.
4. Validate and plan the app, then publish an immutable version and its pinned consumer. Store the one-time consumer execution token through Hostlier's hidden-input secret command.
5. Run contract tests, then disposable-provider E2E before enabling live deployments.

The current local adapter tests verify the client half of this contract. They do not establish that a remote app or any provider operation already exists.
