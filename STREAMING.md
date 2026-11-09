# Resumable operation progress

The CLI follows operation progress and reconnects automatically. `--detach` returns immediately; Ctrl-C stops waiting but leaves the job running.

To reattach later:

```sh
hostlier operation ID --watch
```

Use `hostlier operation ID` for a status snapshot. Advanced replay accepts `--cursor ID:SEQUENCE`. Progress goes to stderr; receipts and final results go to stdout.

## Controller transport

The CLI and backend Cloud SDKs use native gRPC over HTTP/2 on the profile’s existing HTTPS address. The CLI also speaks gRPC on the protected local Unix socket. Hostlier’s managed proxy routes `/hostlier.control.v1.Controller/*` to the controller over HTTP/2; a custom proxy must do the same and preserve gRPC trailers. Browser pages, local dashboard SSE, tunnels and HTTP integrations keep their existing routes.

The [service definition](internal/controlrpc/controller.proto) separates commands from subscriptions. A command that starts durable work returns its receipt. `WatchOperation` sends retained logs and status changes; `WatchJob` sends the current planning, approval or recovery receipt and subsequent changes. `WatchStatus` sends complete replacement snapshots for the dashboard. The SDKs only call commands and the operation and job subscriptions needed for their lifecycle methods.

A lost connection reopens the subscription for the same ID. The CLI resumes from its last delivered log cursor; it never switches to polling or resubmits a deployment to obtain its result. Cancelling a subscription releases the observer and leaves accepted work running. A controller process failure still follows the normal interrupted-operation recovery rules.

| Operation event | Payload |
| --- | --- |
| `log` | Sanitized execution log with cursor `{operationId}:{sequence}` |
| `operation` | Current operation snapshot, including the terminal outcome |
| `reset` | A cursor predates retained history; retained logs and current status follow |
| `heartbeat` | Keeps idle subscribers connected and checks authority; no progress payload |

The controller retains the newest 1,000 execution events globally. It validates cursor scope and sequence and checks credentials during a watch. Progress after a retention gap may be incomplete; the terminal snapshot remains authoritative.
