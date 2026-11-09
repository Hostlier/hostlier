# Optional Terraform and Pulumi packages

Research only, 2026-10-29. No infrastructure was changed. The proposed names below are design choices, not existing APIs.

## Recommendation

Keep the controller and CLI in Go. Ship separately installed `hostlier-package-terraform` and `hostlier-package-pulumi` executables through the existing package protocol. Put Pulumi Automation API in a separate Go module so ordinary Hostlier builds do not acquire its dependency graph. Terraform can use a small typed argv/JSON adapter rather than embedding Terraform. Neither integration requires a Node controller; Pulumi can initially support packaged Go programs, with other language runtimes explicit optional requirements later.

Existing foundations are strong: `pkg/packagekit` separates describe/plan/apply/inspect/reconcile, the process runner pins executable bytes, prepared payloads/results are encrypted, and the controller writes `uncertain` before side effects. Preserve the existing interactive deployment approval and recovery boundary.

## Real differences in approval semantics

* Terraform: generate `plan -input=false -out=...`, inspect using `show -json`, then apply that exact saved plan using `apply -input=false <plan>`. Do not run a fresh implicit plan after approval. Preserve the matching toolchain and working directory. A saved plan constrains intended changes; it is not a transaction or immunity to provider failure/live drift. State changes can invalidate it. [Plan reference](https://developer.hashicorp.com/terraform/cli/commands/plan), [workflow](https://developer.hashicorp.com/terraform/cli/run).
* Pulumi: Go Automation API supports `optpreview.Plan(path)` and `optup.Plan(path)`. An ordinary preview followed by `Up` does not bind approval. Saved update plans constrain operations, but enforcement occurs incrementally: earlier batches may already have changed resources when a later discrepancy fails. The plan file format remains experimental. Describe this capability as a constrained update, never Terraform-equivalent exact saved-plan execution. [Update plans](https://www.pulumi.com/docs/iac/operations/stack-management/update-plans/), [preview option](https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview#Plan), [update option](https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/auto/optup#Plan).
* Pulumi Automation API still requires the CLI and provider plugins at runtime. Provider packages and language runtimes must be installed/pinned during explicit preparation, not downloaded unexpectedly inside an approved deployment. [Automation API](https://www.pulumi.com/docs/iac/concepts/automation-api/).

The parent `src/pulumi.ts` has useful separation of runtime preparation, stack identity, sanitized summaries, and secret outputs. Its preview/up pair currently does not save or supply an update plan. Port its boundaries, not a claim that its preview is approval binding.

## Package builder additions

Offer a small fluent builder that produces the current `Handler` and `Describer` interfaces: validated config/output schemas; typed config decoding; explicit Plan/Apply/Inspect/Reconcile callbacks; output allowlists; secret declarations; structured public change summaries; shared conformance tests. Keep engine-specific preparation and execution behind injected interfaces so fixture tests need no binaries.

Prepared metadata should bind package binary digest, program/module content digest, CLI version/checksum, provider/plugin and language lockfile digests, backend identity, stack/workspace identity, operation key, action, config hash and secret revision references. Add an adapter-owned encrypted artifact manifest for raw plan plus program/toolchain inputs. Never accept project-supplied shell commands or arbitrary server paths. Keep private artifacts out of public summaries and provider logs out of protocol stdout.

Use stable ownership derived from controller namespace/application/step; never derive ownership from a release hash. One per-stack/workspace exclusive adapter lease plus native backend locks prevents overlap. A lock is held during engine operations, not an indefinitely open process while waiting for a human approval. Before apply, verify manifest and backend state identity; stale/tampered inputs require a fresh plan and approval.

Preserve engine state in a durable backend; package Result.State stores ownership/backend locator and operation receipt, not a competing state database. Terraform backend locking depends on backend support; never disable locks or automatically force-unlock. Pulumi backends differ in durability/recovery, so advertise a tested backend support matrix and retain native checkpoint history. [Terraform state locking](https://developer.hashicorp.com/terraform/language/state/locking), [Pulumi state/backends](https://www.pulumi.com/docs/iac/concepts/state-and-backends/).

Inspection must distinguish recorded state from live observation. Pulumi does not automatically refresh; `refresh --preview-only` detects drift without committing it. State-changing refresh/adoption is a separate approved operation, not a hidden read-only inspect. A re-plan after drift needs new approval. [Pulumi drift](https://www.pulumi.com/docs/iac/operations/stack-management/drift/).

On crash or timeout, retain `uncertain`; inspect native operation history/state and the persisted operation receipt. Report unknown when completion cannot be proved. Do not blindly retry apply, run destroy as rollback, or interpret an empty stack as successful ownership removal. Destroy needs its own reviewed capability; do not assume Pulumi Up plans also constrain Destroy.

## Gaps to resolve before implementation

1. `prepareWorkflow` currently has a 25-second deadline, the runner 30-second plan / 15-minute apply limits, and stdin/stdout are capped at 4 MiB. Real initialization and planning can exceed these. Add durable planning jobs with bounded, administrator-controlled timeouts and encrypted artifact storage, rather than growing opaque base64 protocol messages without bound.
2. Runner digest pins only the wrapper executable. An external CLI, provider, module, or Pulumi program can change independently. The manifest must cover all executable inputs and fail closed when anything changes.
3. Current retained workflow dependencies reject changed Step fingerprints and reuse succeeded results. A useful IaC update package needs an explicit reviewed update lifecycle for the same ownership key. Using new step identities for each update would accidentally create new stacks. Do not silently weaken all workflow reuse semantics.
4. Workflow inputs resolve predecessor outputs only during execution. Plans cannot claim exact approval of a dependent IaC graph when input values are still unknown. Initially require fully known IaC planning inputs; later add staged plan/approval after predecessor results are available.
5. Native preview can write backend metadata/configuration or run arbitrary user code. Permit only administrator-installed programs with provider-only side effects. Treat read-only planning as an explicit package contract, not a sandbox guarantee.

## Credential-free local E2E matrix

| Layer | Fixture | Evidence |
|---|---|---|
| Shared package contract | Injected engine + temporary state dir | describe/schema, deterministic plan identity, apply only after approval, secret redaction, oversized artifacts, binary/toolchain tampering |
| Terraform real CLI | Local backend + built-in `terraform_data`, no provisioners | init/plan/show/approved saved apply, outputs, no-op, destroy plan/apply, invalidated plan after competing apply; no downloaded provider or cloud credentials |
| Pulumi real engine | File backend + passphrase secret provider + prebuilt Go program with component resources/exports | preview saved plan, constrained Up, secret output filtering, no-op, distinct stack ownership; requires preinstalled CLI/runtime but no cloud token |
| Pulumi local provider | Pinned local test provider that writes only under test temp dir | real CRUD/drift and deliberately changed program causing plan mismatch after earlier completed work; component-only fixture cannot prove provider mutation safety |
| Recovery/concurrency | Kill wrapper after native completion before response; concurrent same-stack jobs | durable uncertain marker, inspect/reconcile adopts only proven result, no blind replay, lease/native-lock conflicts |
| Isolation | Separate owner keys / malicious paths / changed backend | no ownership crossover, fail-closed manifests, no unauthorized credential inheritance |

Tests must run offline after an explicit toolchain preparation step. Missing prerequisites should produce a clear gated integration-test skip; they must not turn a fake engine test into a claimed real CLI E2E. Terraform's built-in fixture is documented in [terraform_data](https://developer.hashicorp.com/terraform/language/resources/terraform-data).

## Minimal delivery sequence

1. Builder ergonomics and engine-independent conformance fixtures; no engine dependencies in controller.
2. Bounded encrypted artifacts, resumable planning and explicit same-owner update semantics; prove approval/recovery invariants.
3. Terraform optional module with local-backend fixture, exact saved-plan execution and pinned offline toolchain.
4. Pulumi optional Go module with explicit constrained-plan capability, file-backend fixture and local-provider partial-failure tests.
5. Add selected remote backends/providers only after their lock, identity, recovery and credential tests pass. Keep provider autoscaling policy packages separate from application workload/sidecar execution; installation/update of such policies remains an approved deployment.
