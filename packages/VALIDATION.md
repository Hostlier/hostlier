# Package workflow verification — 2026-10-29

Verified locally in this workspace:

- `go test -race ./internal/controller ./internal/cli -run '^TestWorkflow' -count=1`: passed. Controller tests execute actual temporary package subprocesses; the CLI integration uses the real controller handler with an in-process transport and a recording deployment runtime.
- `go test -race ./internal/controller ./internal/cli -skip 'Test(DualListeners|RealTLS|Operation(Client|Event)|Packaged)' -count=1`: passed, including existing non-listener controller and CLI regressions.
- Race-enabled tests for `./pkg/workflow`, `./packages/...`, `./internal/config`, and `./cmd/hostlier-package`: passed.
- `go vet ./...`: passed.
- `go run ./cmd/quality .`: passed with no missing comments or functions above complexity 10.
- Static Linux amd64 builds of `hostlier`, `hostlier-package`, `hostlier-aiven`, and `hostlier-railway`: passed. Build artifacts were written to a temporary build directory, not installed on a server.
- Both new `.hostlier` examples pass the actual `hostlier validate` command.
- `go list -deps ./cmd/hostlier-controller` contains no `hostlier/packages/...` imports: provider adapters remain outside the controller binary.

The broader controller and packaged CLI suites encountered sandbox listener restrictions (`bind: operation not permitted`). TLS, socket/PTY, and event-stream tests were excluded from the non-listener regression run; this document does not claim the full suite passed. Existing unrelated tests and sandbox policy were not modified.

Usefused request tests use an in-memory HTTP transport and verify immutable consumer selection, token handling, matching execution identities, readiness, rejection of unsupported modes, endpoint changes after planning, and failure without retries or diagnostic leakage. They do not call provider APIs or deploy real resources.

Remote discovery remains unverified: `fused-cli service list` and `whoami` returned `engine_unavailable`. An unauthenticated health probe returned HTTP 200 and indicated the Unified App worker was ready; a subsequent probe failed DNS resolution. No hosted database Unified App was published, no provider selectors were guessed, and no cloud database was provisioned.
