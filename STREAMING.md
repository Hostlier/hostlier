# Resumable operation progress

The CLI follows operation progress and reconnects automatically. `--detach` returns immediately; Ctrl-C stops waiting but leaves the job running.

To reattach later:

```sh
hostlier operation ID --watch
```

Use `hostlier operation ID` for a status snapshot. Advanced replay accepts `--cursor ID:SEQUENCE`. Progress goes to stderr; receipts and final results go to stdout.

## HTTP contract

`GET /v1/operations/{id}/events` accepts `Accept: text/event-stream` and an optional `Last-Event-ID: {id}:{sequence}`. It uses the same bearer authentication or protected local Unix socket authority as the ordinary API. Credentials are rechecked while streaming; revocation closes an existing subscription.

| SSE event | Data | Replay ID |
| --- | --- | --- |
| `log` | Sanitized execution log with timestamp, level, message, operation ID and sequence | `{operationId}:{sequence}` |
| `operation` | Current operation snapshot; sent initially, on status changes and at completion | None |
| `reset` | `{"reason":"history_expired"}` before available replay when a resume cursor predates retention | None |

Log sequences are durable and monotonically increasing across the controller's event history. Cursor scope is validated against the requested operation. Malformed, cross-operation and future cursors are rejected before stream headers are sent. Legacy stored events receive sequences during state migration without reordering their history.

The controller retains the newest 1,000 execution events globally. Resumption does not provide an unlimited audit history: an expired cursor produces a reset notification, followed by retained events and the current operation state. A new subscription without a cursor receives retained history, which may also be incomplete for older jobs. The terminal operation snapshot remains the completion result.

Connections rotate after roughly 20 seconds, with comment heartbeats every five seconds. The client reconnects until a terminal result or local cancellation. Each write has a short deadline; the controller admits at most 32 simultaneous subscriptions. Network writes happen outside the controller state lock, and new replay cursors are saved before they are exposed. Frames are bounded on the client and acknowledged only after successful consumption.

SSE does not remove network or proxy timeouts. Reconnection and durable server-side operations make progress observation independent of any one HTTP connection. Controller process failure still follows Hostlier's existing interrupted-operation recovery rules; SSE does not automatically replay uncertain provisioning actions.

## Local verification

Tests exercise authenticated streaming, stable replay across controller restart, retained-history resets, invalid cursors, revocation, cancellation, slow-reader capacity, persistence failure, parser limits and secret redaction. A CLI-level TLS test runs plain `deploy`, drops an event stream mid-frame, returns a temporary gateway error, and verifies automatic cursor resumption, one approval/apply, no duplicate logs and one terminal result. The cross-package HTTPS test deliberately ends the first stream after a log, verifies a cursor-bearing reconnect, and confirms that the accepted deployment completes exactly once. The installed ARM64 k3s gate also passes approved deployment and restart with default SSE observation, immediate HTTPS verification and Redis persistence. Explicit installed `operation ID --watch` replay produces one terminal stdout result and cursor-bearing stderr progress.

```sh
go test -race ./internal/client ./internal/cli ./internal/controller
make check
```
