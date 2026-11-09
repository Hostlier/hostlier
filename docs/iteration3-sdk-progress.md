# Iteration 3 SDK capability discovery

Implemented static capability discovery for installed native PostgreSQL and Redis packages. The contract publishes version, package identity, configuration/output schemas, inspection support, output names, and supported secret alias names. Discovery runs through the verified, bounded executable snapshot runner with a minimal environment and no execution request or private inputs. CLI `package describe NAME --state-dir DIR` operates offline.

Split installation handling from package command dispatch and separated discovery filesystem assertions to satisfy the complexity gate. Corrected the package command usage text to include discovery.

Validation:
- Real PostgreSQL executable discovery passes, including unchanged package-directory contents and inherited-secret sentinel checks.
- CLI install/describe integration passes using a compiled production PostgreSQL command without a controller profile.
- `go test ./internal/packageexec ./packages/engines ./pkg/packagekit` passes.
- Focused offline CLI package tests pass.
- Full CLI package suite cannot run inside this sandbox because existing HTTPS tests require loopback listeners (`bind: operation not permitted`).
- Quality scan finds no SDK/discovery violations; the current unrelated failure is runtime `prepare` complexity 11 in `internal/runtime/k3s_deployment.go`.

Read-only review of recovery, scaling and usage found no blocking issue in the reviewed paths. Recovery binds inspection and approval to original prepared inputs, digest, actor and state generation; scaling reapplies explicit flags to fresh desired state. One minor consistency opportunity: `applicationUsage` releases the controller lock and then returns previously captured desired limits/replica settings without checking whether application state changed during observation. A concurrent deployment can therefore pair current measurements with older desired settings. Consider documenting snapshot semantics or detecting generation drift if coherent desired/observed views are required.
