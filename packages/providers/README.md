# Direct managed workflows

`hostlier-aiven` and `hostlier-railway` run direct workflows by default. Railway managed `new-instance` provisioning is unavailable in the direct implementation; its executable supports existing connection workflows. A user-connected Fused Engine adapter is deferred. The existing code can select an experimental legacy Usefused executor through an adjacent `aiven.json` or `railway.json`; that prototype is not a supported alternative for current setup.

The direct executables publish a v1 `describe` capability for local package enrollment and use the shared packagekit protocol server for `serve`. A legacy Usefused executor configuration does not publish that capability.

Aiven direct `new-instance` currently supports PostgreSQL and Valkey. It requires explicit `instance.project`, `instance.service`, `instance.plan`, `instance.cloud`, and a protected `apiToken` secret alias. Planning reads the selected service, regional hourly base price and persistent plan capacity. Applying repeats these reads, rejects approval drift, creates at most once with a `hostlier-owner` tag and termination protection, then polls readiness. Reuse requires encrypted prior state matching the service's creation timestamp and tag. A same-name service is never adopted. The controller must retain its uncertain-operation marker when creation or readiness fails.

Each provider HTTP call has a 30 second deadline; controller planning imposes its shorter shared deadline. Aiven readiness waits for up to 10 minutes within the workflow runner's overall apply deadline. A failed readiness read aborts the invocation and retains uncertain state; it does not issue another create. No cloud provisioning was exercised during local verification.

The output `serviceId` is a project/name identity; Aiven's documented service response does not expose a separate immutable UUID. The creation timestamp and ownership tag supplement this identity. No service deletion is automated.

The service's administrative connection and project CA are **private outputs** (`connection`, `caCertificatePEM`). Feed both into a subsequent `postgres` new-database or `redis` existing workflow step. Only that step's scoped connection may bind to application environment. The direct provider never returns an application `url`.

Both providers support existing TLS PostgreSQL and Redis/Valkey administrative connections by delegating to the native engine workflow. Use `engine: postgres`, `mode: new-database`, `database: app`; or `engine: redis`/`valkey`, `mode: existing`, optional `keyPrefix`. `instance` is absent in these modes. Protected aliases and their validation follow the native engine package.

Direct Railway `new-instance` deliberately fails before contacting the provider. A generic Railway template does not prove database TLS, persistent storage, or permission to create scoped users. This implementation does not turn a TCP proxy into an assumed TLS endpoint. Use an existing verified TLS connection while managed provisioning remains unavailable. This is an implementation limitation, not a claim that Railway cannot supply such a deployment.

Official contract references consulted:

- [Aiven OpenAPI](https://api.aiven.io/doc/openapi.json), retrieved 2026-10-29: `ServiceCreate`, `ServiceGet`, `ProjectServicePlanPriceGet`, `ProjectServicePlanSpecsGet`, and `ProjectKmsGetCA`.
- [Aiven API documentation](https://rest.aiven.io/doc/).
- [Aiven Valkey TLS settings](https://aiven.io/docs/products/valkey/reference/advanced-params).
- [Railway public API](https://docs.railway.com/integrations/api).

Contract tests inject HTTP responses and never create billable cloud resources. Real provider provisioning, readiness and downstream scoped-user creation require separate credentialed testing.
