# Private GHCR fallback validation

Observed on 2026-11-10 using
`ghcr.io/usefused/margin-central/server:v1.1.29` and a freshly built Hostlier CLI.

The real controller runtime was configured without a Docker login or registry
credentials. Its anonymous GHCR request failed with `IMAGE_RESOLUTION`. The CLI
then used the laptop's existing Docker credential helper, downloaded the image
and uploaded it automatically through the production gRPC API over verified TLS.
Registry credentials were not passed in arguments, logged, or sent to the controller.

The project used top-level `image`, omitted architecture, and selected port 3011.
Controller discovery selected `linux/arm64` from the multi-platform image. The
default `margin-central serve` command ran without production secrets or
dependencies; its SQLite path was redirected to disposable `/tmp` storage.

| Check | Observed result |
| --- | --- |
| Controller-first request | One real anonymous GHCR failure, `IMAGE_RESOLUTION` |
| Automatic local fallback | Existing laptop registry login; no manual pull, save or upload |
| Uploaded archive | 40,712,704 bytes across 39 actual chunk requests |
| Deployment | Succeeded; container running |
| Caddy application route | HTTP 200 |
| Registry index digest | `sha256:ecbea3768bbef7d6d3160b2b6c01562007a43c162a03870a4633e800e0696acf` |
| Selected ARM64 manifest | `sha256:4f46e8a8b82079bc1936107aa7000062d7d5f1aeddc2d624c07d527c5d13417c` |
| Registry and imported config digest | `sha256:4b3cf2b51a22dfcf51edc833ba2d77b7c83e111d6ea8d8ca6d979dbda06105c3` |
| Prepared artifact | `hostlier.local/artifacts@sha256:6130521f848e41041259cd410dfda26a10794935e473fa5f1218e9ee9a889518` |

The test rehashed the controller's retained archive, compared its config digest
with the separately inspected immutable registry manifest, checked the running
container's actual Docker image identity, and streamed a Docker export to verify
the imported config bytes. This supports both Docker's classic image store and
the containerd store, where container inspection exposes a manifest ID.

The first run found a real legacy deployment bug: Docker container creation used
the prepared artifact reference rather than its imported local tag. The fix maps
that reference through `artifact.RuntimeImage`, as version 2 deployments already
do. A focused regression test covers this daemon request.

The final live run passed with the Go race detector, along with the affected CLI
registry, container and artifact tests. The complete runtime race suite and
`go vet ./internal/cli ./internal/runtime` also passed.

## Repeating the live test

Use a disposable Docker daemon, a trusted local Caddy executable, Go, Python 3,
and an existing local Docker login with access to the chosen private image:

```sh
HOSTLIER_PRIVATE_REGISTRY_E2E=1 \
HOSTLIER_PRIVATE_IMAGE=ghcr.io/usefused/margin-central/server:v1.1.29 \
HOSTLIER_PRIVATE_IMAGE_PORT=3011 \
HOSTLIER_CADDY_BINARY=/absolute/path/to/caddy \
go test ./internal/cli -run '^TestPrivateRegistryLiveFallback$' -count=1 -v -timeout=18m
```

Optionally set `HOSTLIER_DOCKER_SOCKET` to the test daemon's Unix socket and
`HOSTLIER_PRIVATE_IMAGE_CONFIG_DIGEST` to an independently inspected config
digest for the expected controller platform. `TMPDIR` may select a private test
volume with adequate free space; the artifact store's disk reserve stays enabled.
The test creates fresh controller state and CLI profiles, removes its owned
containers, networks and volumes, and removes imported artifact tags that did
not exist before the run. It preserves existing registry logins and cached tags.

## Scope

This ran a real controller in the Go integration process on macOS ARM64, real
Docker Desktop Linux ARM64 containers, and Caddy 2.10.2. Controller transport used
verified TLS; the application edge used disposable loopback HTTP with the public
HTTPS probe explicitly skipped. Approvals used the existing independent software
test signer through the controller's normal cryptographic verification API and
the CLI's real terminal confirmation. The test does not certify an installed
systemd service, physical hardware-key interaction, public DNS/ACME, AMD64
execution, network-outage fallback, or a private-image rollout on k3s.
