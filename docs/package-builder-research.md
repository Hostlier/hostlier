# Executable package SDK: code-grounded proposal

Research only; no package, infrastructure, or protocol implementation changes. This complements [Terraform/Pulumi research](terraform-pulumi-research.md) and [autoscaling research](autoscaling-research.md).

## Start from the existing protocol

Hostlier already has the right extension boundary: administrator-installed executable packages, outside the controller binary. Improve `pkg/packagekit`; do not introduce a parallel package framework.

| Existing code | Reuse and remaining gap |
| --- | --- |
| `pkg/packagekit/{types,serve,protocol,describe}.go` | `Handler.Handle`, `Describer.Capability`, strict bounded single-document exchange and response validation already exist. Authors still implement action dispatch, config decoding, fingerprints and metadata manually. `Describe` only checks basic contract completeness; typed schema/runtime validation should share one definition. |
| `pkg/workflow/{types,lifecycle}.go` | `Request`, encrypted opaque `Prepared.Payload`, bounded public `Summary`, private `Result`, `Inspector`, and three authoritative inspection statuses already exist. Wire outputs are strings; preserve this and encode typed author-facing outputs through an explicit codec. Capability currently advertises only `Inspect`, not individual lifecycle operations. |
| `internal/packageexec/{runner,install,inspect}.go` | Protected executable install, SHA-256 identity, immutable executable snapshots, pinned inspection, clean environment, discarded stderr, bounded pipes and deadlines already exist. The digest pins executable bytes, not adjacent CLI binaries, providers, modules or backend configuration. No protected per-operation working-directory/toolchain manager exists here. |
| `packages/engines/capability.go`, `packages/providers/package.go` | Native engines publish handwritten schemas. Provider packages repeat strict config decoding, request fingerprints and a separate `Serve`; direct providers currently accept only plan/apply. Migrate incrementally to shared helpers without implying every existing package supports recovery. |
| `pkg/packagekit/contracttest`, `test/e2e/workflow_local_test.go` | Reusable in-process protocol exchange/malformed-input checks and local workflow E2E are available. Extend these rather than inventing a separate test harness. |

The runner uses a 30-second plan deadline and a 15-minute apply deadline. `packagekit.Serve` accepts at most 1 MiB of request data while the runner permits 4 MiB. Establish an intentional common bound before introducing larger handles; keep saved plans outside protocol JSON.

The root TypeScript SDK (`src/index.ts`, `src/product-definition.ts`, `src/project.ts`) exposes `defineProduct`, `defineProject`, hosting operations and Pulumi project packaging. It describes deployable applications and their instances/dependencies. The Go executable package SDK describes trusted lifecycle adapters invoked by Hostlier. Keep these authoring surfaces separate; products select installed package capabilities through workflow steps. A Terraform adapter is neither a new product-definition DSL nor permission to accept arbitrary project-supplied shell commands.

## Small reusable authoring layer

Add a generic `packagekit.Definition[C, P, S, O]` implementing the existing `Handler` and `Describer`. Suggested authoring types are decoded config, prepared payload, persisted state and outputs. Keep `workflow.Request/Response` as the transport. Require explicit schemas/codecs, validate config before callbacks, reject undeclared secret aliases, and validate outputs before publication. Do not pretend Go reflection alone expresses constraints such as allowed regions, replacements or credential scope.

A shared `PlanContext` should supply the stable request key, resolved private secrets/inputs, previous state, protected workspace and an allowlisted tool executor. `ApplyContext` receives a validated prepared artifact, not an arbitrary filesystem path. Helpers should canonicalize and bind config/input identity while keeping private values out of diagnostics. Package-specific ownership, readiness, TLS and resource reconciliation remain adapter responsibilities.

**Proposed API sketch (illustrative; these symbols do not exist today):**

```go
pkg := packagekit.Define(packagekit.Definition[Config, PlanRef, State, Outputs]{
    Name: "terraform",
    Config: configCodec, // typed decode + explicit JSON Schema + validation
    Outputs: outputCodec, // explicit mapping to workflow string bindings
    SecretAliases: []string{"provider", "backend"},
    Capabilities: packagekit.Capabilities{Inspect: true},
    Toolchain: packagekit.BundleRef{
        Version: "adapter-release-1", Platform: "linux/amd64", SHA256: bundleDigest,
    },
    Plan: func(ctx context.Context, p packagekit.PlanContext[Config, State]) (
        packagekit.Plan[PlanRef], error,
    ) {
        saved, err := p.Tools.Terraform.Plan(ctx, p.Workspace, p.Config)
        if err != nil { return packagekit.Plan[PlanRef]{}, err }
        return packagekit.Plan[PlanRef]{
            Prepared: PlanRef{Artifact: saved.Handle},
            Summary: saved.RedactedSummary,
        }, nil
    },
    Apply: applySavedPlan,
    Inspect: inspectOwnedResources,
})
// Existing transport remains the executable boundary.
err := packagekit.Serve(ctx, pkg, os.Stdin, os.Stdout)
```

`PlanRef` must reference immutable host-managed storage, not contain saved-plan bytes or credentials. The host validates handle ownership, operation identity, expiry and content hashes before use. Keep existing `Prepared.Digest` semantics (installed executable digest); add separately named artifact/toolchain identities instead of silently repurposing it.

## Toolchain and artifact contract

* Bundle manifests pin adapter, CLI version/hash, OS/architecture, Terraform provider lock/module source digests or Pulumi language dependencies/provider plugins and source bundle. No ambient `PATH` lookup or automatic unreviewed downloads during apply. The immutable executable snapshot currently lives separately from any tool bundle: explicitly resolve the manifest through protected host storage.
* Create owner-only working directories under a host-selected state root. Reject traversal and symlink escapes, isolate concurrent operation directories, hold backend/stack locks and use scoped backend/provider credentials. `HostContext.StateDir` is host supplied, but should not become a project-selectable working directory. A subprocess is not a security sandbox: packages and their toolchains remain trusted administrator-installed code.
* Separate durable encrypted artifacts/state from disposable workspaces. Saved plans, stack state, credentials and diagnostic files can contain secrets. Supply credentials transiently with an allowlisted environment/file mechanism; redact summaries by construction and retain bounded private diagnostic records separately from public progress. Do not put tool stdout onto protocol stdout.
* Bind artifacts to request key, config/private-input fingerprint, executable and bundle identities, source/dependency digests, backend/stack identity and relevant state revision. Define retention, expiry, cleanup after failure, and recovery across controller restarts. A changed identity requires fresh planning/approval.
* Expose a bounded public review of create/update/delete/replace/import counts, resource identities, exposure, destructive changes and known/unknown cost information. Sensitive or unknown values must remain labeled rather than rendered. Approval binds both the artifact and review, not an editable command string.

A digest-pinned binary is not an exact infrastructure plan guarantee. Terraform's saved plan is the executable plan artifact, but state drift, credentials, provider behavior and external services can still cause failure. Pulumi Automation API supports saved update plans through `optpreview.Plan` and `optup.Plan`, but enforcement is incremental: earlier operations can complete before a later discrepancy fails, and the plan format remains experimental. Advertise constrained update separately from preview-only/revalidation capabilities; invalidate approval when relevant intent changes. The companion Terraform/Pulumi research links the upstream contracts. Never silently run an unconstrained fresh plan at apply and claim it is the reviewed plan.

## Lifecycle capabilities and long operations

The companion IaC review identifies two controller integration prerequisites: retained successful workflow steps currently reuse results and reject changed step fingerprints, so same-owner infrastructure updates need an explicit reviewed lifecycle; changing step IDs would create unintended new ownership. Predecessor outputs resolve during execution, so initial IaC plans must require known inputs or stage planning/approval after predecessors complete. Also account for the controller's 25-second workflow preparation deadline, not only runner timeouts.

Keep protocol v1 synchronous for initial packages. Add negotiated capabilities and versioned transport changes only when needed:

* `inspect` remains read-only and returns existing `completed`, `not_started`, or `uncertain`; uncertain outcomes never authorize blind replay. Observation must prove ownership and requested outcome before publishing outputs.
* `reconcile` needs an explicit meaning: v1 accepts the action but the runner has no distinct reconcile method. Distinguish read-only recovery from any corrective mutation; a mutating repair needs its own reviewed intent.
* Import/adoption and destructive resource cleanup/destroy are explicit opt-in capabilities with independent plans and approval. Workspace garbage collection is different from deleting resources. Application deletion must not implicitly destroy infrastructure.
* Long-running planning/apply need durable operation handles, bounded polling/progress events, cancellation semantics, restart recovery and configurable host ceilings. Do not stream extra JSON documents through current `Serve`; negotiate a protocol extension or a separate protected event channel. Async initiation must record intent/identity before side effects; cancellation cannot promise rollback.

## Local verification and rollout

Extend `contracttest` with typed-codec/schema parity, unknown config/secret rejection, correct response variants, output validation and secret-free public summaries. Exercise actual built executables through `packageexec.Runner` in temporary protected directories, not only direct callback tests.

A local E2E fixture should use an instrumented fake toolchain/backend: plan saves an artifact, explicit approval references it, apply consumes exactly that artifact, inspection restores outputs after a lost apply response, and controller restart reuses the operation. Assert binary/bundle/source/config/backend/state revision changes invalidate the plan; swapped/cross-operation/expired handles fail; concurrent operations cannot share workspace/locks; uncertain inspection prevents retries; cancellation and malformed/oversized output leave recoverable state. Include sentinel credentials in requests/tool logs and assert none reach summaries, CLI output or progress. Test import/destroy rejection when capabilities are absent. Run real Terraform/Pulumi local-backend fixtures separately to establish adapter-specific plan semantics; fake tests cannot certify those semantics or cloud providers.

1. Add codecs/definition helpers and contract tests without wire changes. Migrate one native engine and remove duplicate provider transport only after parity tests.
2. Add protected toolchain/workspace/artifact storage and one narrow Terraform adapter. Preserve existing controller approval, encrypted prepared payloads and pinned executable recovery.
3. Add Pulumi with an honest preview/update-plan capability contract. Introduce durable async operations and progress through negotiated protocol changes when synchronous limits prove insufficient.
4. Add import, corrective reconcile and destructive cleanup separately, each with ownership checks, approval and failure-recovery tests. Keep autoscaling decision ownership in the Hostlier controller as proposed in the companion autoscaling review: recommendation-only first, then explicitly approved bounded delegation and a journaled replica mutation contract. Packages may install approved infrastructure/policy resources, but must not become a competing replica writer or imply authorization to purchase cloud capacity.
