# Hostlier Cloud SDK for Go

Install the Go client from the Hostlier CLI into an existing Go module:

```sh
hostlier sdk install cloud-go --directory ./backend
```

The CLI copies the SDK source into `backend/hostliercloud` and adds the gRPC, protobuf and YAML requirements and checksums if needed. It does not use npm, fetch the SDK, or contact a controller. The project needs Go 1.24 or newer to build and may need normal Go module access to download these dependencies.

Enroll a dedicated service device with `hostlier connect` using the product owner's account and grant it the required controller access. Export its signed credential once and provide the private file to the backend:

```sh
hostlier sdk credential export --server production --output /run/secrets/hostlier-sdk.json
```

The exported file has mode `0600` and contains the HTTPS controller URL, device token, and Ed25519 signing key. A one-time pairing token or legacy bearer profile cannot authorize SDK calls. The CLI is needed for enrollment, export, and credential rotation; the Go SDK calls the controller directly at runtime.

```go
import (
    "context"
    cloudsdk "example.com/backend/hostliercloud"
)

func provision() error {
    client, err := cloudsdk.Open(cloudsdk.Options{
        BundlePath: "./vendor/.hostlier",
        StateDirectory: "./state/hostlier-instances",
        CredentialFile: "/run/secrets/hostlier-sdk.json",
    })
    if err != nil { return err }

    _, err = client.Create(context.Background(), cloudsdk.CreateOptions{
        Name: "acme-app", Route: "acme.apps.example.com",
        Environment: map[string]string{"CUSTOMER_ID": "acme"},
        Secrets: map[string]string{"API_KEY": "secret://acme-api-key"},
        SidecarSecrets: map[string]map[string]string{"storage": {"ROOT_ACCESS_KEY_ID": "secret://acme-storage-access"}},
    })
    if err != nil { return err }
    _, err = client.SetCompute(context.Background(), "acme-app", cloudsdk.Compute{CPU: "0.5", Memory: "512Mi"})
    if err != nil { return err }
    _, err = client.SetEnvironment(context.Background(), "acme-app", map[string]string{"FEATURE_ENABLED": "true"})
    return err
}
```

Replace `example.com/backend` with the module path reported by the install command. The SDK uses the same signed requests, plans, retained approvals, and operations as the Hostlier CLI. Provisioning needs an owner-approved all-access grant for dynamic application names, secrets, and purge. The credential file and instance state directory must be private and durable.

The SDK uses native gRPC on the existing controller URL. It watches approvals, plans and operations on server streams and resumes interrupted observation without submitting the operation again. Upgrade the controller and its HTTP/2 proxy before reinstalling the SDK; there is no HTTP fallback.

The vendor's `.hostlier` provides the image and complete application configuration. New instances override only customer identity, route, optional environment, compute, and declared stored-secret bindings. `Secrets` and `SidecarSecrets` accept only `secret://NAME` references for keys already declared in the bundle. `SetSecret(ctx, name, value)` writes a controller-wide stored secret through the controller API. For a customer's private secret, use `SetInstanceSecret(ctx, appName, name, value)`: the controller records the owning application, rejects use by other apps, and removes the encrypted secret and upload receipt after a successful app purge. Values never enter arguments, project files, or SDK state. A successful operation saves the controller's resolved image digest and complete desired project. A later change checks for out-of-band edits and refuses to overwrite them.

If the same controller identity repeats an instance-secret registration with the same owner and value while its completed approval is still recent, the controller returns the completed receipt without creating another approval or secret revision. A different value or identity follows the normal approval flow.

`DeleteSecret(ctx, name)` remains available for explicit cleanup of controller-wide secrets. The controller refuses deletion while an active deployment, retained rollback revision, or operation still references the secret. Repeating the call is safe. Instance-owned secrets are removed by the controller with the app purge.

`StatusSnapshot(ctx, name)` returns typed controller status, public URL, and `Ready()` for a running deployment. Interrupted operations retain `pending.json`; `RecoverPending(ctx, name, expectedImageDigest)` can clear it only after the controller reports a ready application with the exact desired project and a separately reviewed image digest. It never resubmits a mutation. If the project differs or the digest cannot be verified, reconcile manually.

`Purge(ctx, name)` submits a permanent delete through the controller API. An enrolled service device with an owner-approved all-access grant can run scheduled expiry cleanup unattended. The SDK retires local state only after the controller reports success. If the result is interrupted, the pending intent blocks further changes; `ConfirmPurge(ctx, name, operationID)` requires the controller to report that exact application's `delete` operation succeeded with `purge: true`. A retired instance name cannot be reused in the same state store.

`RecoverPendingPurge(ctx, name)` supports automatic worker recovery: it compares the controller's operation inventory with IDs saved before the original request. It completes state retirement only for one new successful purge and an absent app. If no operation was accepted and the unchanged app still exists, it retries the purge through the controller API. Other ambiguous outcomes stay pending.

`ExpandSidecarVolume(ctx, name, "storage", "data", "10Gi")` submits a complete Hostlier update that increases one declared sidecar volume without changing the pinned bundle identity. It rejects equal sizes and shrinking. Hostlier runtime support for expansion varies by storage backend, and a declared volume size is not an enforced quota on Docker or local-path Kubernetes storage; the application needs a separate write limit to enforce a subscription cap.

Image-based v1 and v2 bundles with inline configuration are supported. Local build sources, file-backed managed configs, and project package hooks are excluded. The SDK preserves inline configs, health, sidecars, volumes, dependencies, workflow, and secret bindings. Hostlier's usual plan, approval, and scope rules still apply; the Go SDK does not grant automation authority.
