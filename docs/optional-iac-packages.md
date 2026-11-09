# Optional Terraform and Pulumi executable packages

The controller imports neither engine SDK. `packages/terraform` uses pinned direct CLI argv; `packages/pulumi` is an independent Go module using Pulumi's Go Automation API. Their commands serve the existing package protocol. These packages manage infrastructure; workload execution, sidecars and replica decisions stay with their existing controller paths.

The first supported native backends are Terraform **local** and Pulumi **file + passphrase**. Remote backends, import, refresh/adoption and destroy are not exposed. Native checkpoint directories are durable and private under the host-selected state directory. Terraform checkpoint contents may contain plaintext secrets, protected by the owner-only backend directory; Pulumi encrypts native secret values using its passphrase provider. Back these directories up with the host state. Infrastructure is not destroyed when an application or its working directories are removed.

## Administration and installation

Prepare all CLI, provider, program and dependency bytes explicitly before installation. Deployment performs no installer/download step. Pin and verify official archive checksums, then produce the complete file manifest with `scripts/iac/catalog.py`. Never point a bundle at an editable checkout, symlink or group/world-writable directory. The complete source/runtime manifest is revalidated before native operations. Trusted programs and providers remain administrator-installed executable code: preview is a contract, not an operating-system sandbox against malicious provider/program code.

Supported runtime versions in this implementation are Terraform **1.9.8**, Pulumi CLI/Go SDK **3.142.0**. The actual CLI versions are verified before use. Terraform provider binaries must be preinstalled under `plugins/`; include `.terraform.lock.hcl` and all module source files in the manifest when needed. `init -get=false -lockfile=readonly -plugin-dir=...` prevents ordinary provider/module acquisition. The credential-free built-in `terraform_data` fixture needs neither a provider download nor a lockfile.

A Terraform bundle contains:

```
terraform                    # pinned executable
plugins/                     # existing directory, can be empty for built-in provider
program/main.tf              # administrator-reviewed module
program/.terraform.lock.hcl   # when external providers are used
```

A Pulumi bundle contains:

```
pulumi
pulumi-language-go
go                           # pinned actual Go executable for language-host dependency discovery
goroot/VERSION
goroot/pkg/                  # directory for the minimal discovery runtime
home/plugins/resource-PROVIDER-vVERSION/pulumi-resource-PROVIDER
program/Pulumi.yaml
program/app                  # pinned prebuilt Go program
```

Pulumi `Pulumi.yaml` uses the Go runtime's `binary: ./app` option. The current Go language host still invokes the Go CLI for dependency discovery even with a prebuilt program. Pin the Go executable and its matching `GOROOT/VERSION`; the tested prebuilt fixture does not require the compiler/standard library or module acquisition at runtime. The adapter supplies a private pinned discovery root and `GOTOOLCHAIN=local`, `GOPROXY=off`, `GOSUMDB=off`, `GOENV=off`, and disables automatic Pulumi plugin acquisition. A different program/runtime may require additional explicitly pinned runtime files. Do not assume arbitrary source-based Go programs work with the minimal fixture runtime.

Remove non-owner write permissions from all prepared entries (for example, `chmod -R go-w /opt/hostlier/iac/terraform-1`). Generate non-secret catalogs with finite output declarations and credential mappings:

```sh
python3 scripts/iac/catalog.py --root /opt/hostlier/iac/terraform-1 --name local --outputs value,private > terraform.json
python3 scripts/iac/catalog.py --root /opt/hostlier/iac/pulumi-1 --name local --outputs value,private --secret backend=PULUMI_CONFIG_PASSPHRASE > pulumi.json
```

Install catalogs at `/etc/hostlier/iac/terraform.json` and `/etc/hostlier/iac/pulumi.json`, regular files permitting only their administrator owner to write. For another host-managed path, build with `-ldflags '-X main.catalogPath=/absolute/catalog.json'`; the path is fixed in the executable, never a workflow field. Build/install on the controller host:

```sh
go build -o hostlier-terraform ./cmd/hostlier-terraform
hostlier package install terraform --file ./hostlier-terraform --state-dir /var/lib/hostlier
(cd packages/pulumi && go build -o hostlier-pulumi ./cmd/hostlier-pulumi)
hostlier package install pulumi --file ./packages/pulumi/hostlier-pulumi --state-dir /var/lib/hostlier
hostlier package describe terraform --state-dir /var/lib/hostlier
hostlier package describe pulumi --state-dir /var/lib/hostlier
```

Keep all installed bundle versions needed by outstanding approvals/recovery; changing their bytes invalidates the saved identity. Catalogs contain paths/digests and mapping names, never credential values. Credential aliases may map to provider namespaces or `PULUMI_CONFIG_PASSPHRASE`, excluding process/backend/program controls. No ambient controller credentials, `PATH`, username or loader environment are inherited. The executor uses a fixed `USER=hostlier` because Pulumi's file backend requires a user identity.

## Workflow contract

Select a catalog ID and fully known string variables, opting into reviewed same-owner updates with `lifecycle: managed`:

```yaml
steps:
  - id: infrastructure
    package: terraform
    lifecycle: managed
    config:
      bundle: local
      variables:
        value: hello
```

For Pulumi, set `package: pulumi` and bind its administrator-declared `backend` secret alias to a stored backend passphrase. Package input bindings become additional program variables; duplicate config/input names are rejected. A predecessor input must already be resolved when its infrastructure plan is reviewed. Changing the stable step key creates different native ownership; retain the key for updates.

The catalog's finite output list is enforced for each bundle. Native sensitive/secret outputs become encrypted private workflow outputs. They cannot become ordinary application bindings. The protocol currently requires at least one public output; declare/export an appropriate non-secret status or endpoint. Reviews publish constructed aggregate counts and explicitly label costs/exposure unknown rather than rendering provider values.

Terraform executes exactly `apply ... hostlier.plan` using the saved bytes produced by the reviewed plan; it never implicitly replans. Native state revision changes invalidate approval. A saved Terraform plan does not make providers transactional or prevent partial failure.

Pulumi uses `Preview(optpreview.Plan(path))` and `Up(optup.Plan(path))`. This is an experimental **constrained update plan**, with incremental enforcement. It is not Terraform-equivalent exact saved-plan execution: an earlier mutation can complete before a later constraint mismatch fails. The adapter restores the native checkpoint's passphrase salt in each isolated workspace so a saved plan remains decryptable.

Saved plans are encrypted authenticated artifacts bound to owner, operation/private intent, wrapper digest, complete installed bundle, source, native backend and checkpoint revision. No arbitrary path or shell command is accepted through configuration. Plans expire after 24 hours; receipts after seven days. Expired artifacts are refused and physical retention cleanup remains an administrator task.

One adapter lease and native backend locking protect each owner during operations; no lock is held across human review. Apply fsyncs an uncertain intent before native mutation and writes an encrypted success receipt only after successful engine completion/output validation. Inspection never refreshes, reapplies or destroys. It verifies recorded checkpoint/receipt state, not live provider resources; external drift that has not changed the checkpoint remains unobserved. Any refresh/adoption requires a separate explicitly reviewed operation, outside these initial packages. A crash after native completion but before the receipt remains uncertain; an abandoned adapter lock also requires investigation. Do not remove locks or blindly retry without native-state investigation and a new approved plan. A valid completion receipt is adopted only while its recorded checkpoint/toolchain/ownership still match.

## Credential-free real tests

Tests skip clearly unless explicitly prepared CLI/program/provider paths are supplied. They do not fetch prerequisites. Download and verify the official release archives first. For the actual Darwin/arm64 run, official archive SHA-256 values were:

* `terraform_1.9.8_darwin_arm64.zip`: `873d7b925d08578fb6bb9c12c7cd92ae73e289e07c360f2fdd69f9036b7baaab`.
* `pulumi-v3.142.0-darwin-arm64.tar.gz`: `5ee6510204e8a43cb7306a3a88b360801b569934ab816b9e813b97cec0d79644`.

Use the matching official checksum publication for another platform. Sources: [Terraform release and checksums](https://releases.hashicorp.com/terraform/1.9.8/), [Pulumi checksum publication](https://get.pulumi.com/releases/sdk/pulumi-3.142.0-checksums.txt).

```sh
HOSTLIER_TERRAFORM_TEST_CLI=/prepared/terraform go test -count=1 -v ./packages/terraform
HOSTLIER_TERRAFORM_TEST_CLI=/prepared/terraform go test -count=1 -v -run TestTerraformManagedControllerCLI ./test/e2e
go test -race ./packages/iac ./pkg/packagekit/... ./internal/packageexec

cd packages/pulumi
go build -o /prepared/program ./testdata/program
go build -o /prepared/pulumi-resource-local ./testdata/provider
HOSTLIER_PULUMI_TEST_CLI_DIR=/prepared/pulumi \
HOSTLIER_PULUMI_TEST_PROGRAM=/prepared/program \
HOSTLIER_PULUMI_TEST_PROVIDER=/prepared/pulumi-resource-local \
go test -count=1 -v ./...
```

Pulumi requires local loopback TCP for engine/language/provider gRPC. Run its local fixture with that permission, without cloud credentials. The tiny local provider writes only files whose paths include the unique fixture-directory marker. It is test code, never a production provider or installer. Building fixture binaries is an explicit prerequisite using the optional module's pinned dependencies; deployment runs the compiled program.

Verified locally on Darwin/arm64: real Terraform saved create/update/no-op, sensitive-output filtering, saved-plan rejection after a competing approved update, and the actual built/installed wrapper through digest-pinned Runner plan/apply/inspect. The actual controller/CLI Terraform test additionally parses strict YAML, discovers managed capabilities, checks no native mutation precedes interactive approval, binds the native output into the application runtime boundary, restarts the controller and updates the same native resource ID in the same sole backend. Its application runtime is a recording fixture; no Docker application or public ingress is claimed by that test. Real Pulumi create/update/no-op, secret outputs, receipt inspection and replay rejection passed using genuine local provider CRUD. `TestRealPulumiIncrementalConstraintFailure` deliberately bypasses the package request-identity guard to exercise the native plan constraint: its first approved file update completes, its later unplanned update fails, and the second file remains unchanged. Ordinary protocol requests reject changed private intent before reaching that native test scenario.

No cloud-provider or remote-backend conformance is claimed. The fixture establishes native local engine behavior, not safety/durability of untested backends or arbitrary trusted programs.
