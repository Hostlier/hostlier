# Hostlier minimal shared collector

This is a slim distribution of the open-source [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/), built from unmodified upstream components. It includes only the factories used by Hostlier's generated Docker and k3s configurations. It uses the same component releases as the upstream Contrib 0.164.0 binary and currently builds with Go 1.27.3. It supports both runtimes in one image; it does not install a monitoring container in each application Pod.

| Component type | Included factories | Module versions |
| --- | --- | --- |
| Extension | `docker_observer` | Contrib 0.164.1 |
| Receivers | `docker_stats`, `file_log`, `receiver_creator`, `kubelet_stats` | Contrib 0.164.1 |
| Processors | `batch`, `memory_limiter` | Core 0.164.0 |
| Processors | `filter` | Contrib 0.164.1 |
| Processors | `k8s_attributes` | Stable Contrib 1.3.1 |
| Exporter | `otlp_http` | Core 0.164.0 |
| Configuration providers | `env`, `file` | Stable core 1.70.0 |

There is no generic OTLP receiver, debug exporter, third-party backend exporter, or additional monitoring receiver. Transitive dependencies of these components remain linked; Kubernetes discovery and OTTL filtering still contribute substantially to the binary size.

Versions were verified against `go version -m` on the official Linux ARM64 `otelcol-contrib` binary. In particular, Kubernetes attributes is a stable **1.3.1** module, while most Contrib components are **0.164.1**; the distribution and Builder themselves are **0.164.0**. Do not assign one version mechanically to every module.

## Build Linux on a Mac

A working Go installation and network access to the Go module proxy/checksum database are required for the first build. Run from this directory:

```sh
./build.sh arm64
# Optional alternative architecture:
./build.sh amd64
```

The script pins `GOTOOLCHAIN=go1.27.3`, generates factories with OCB 0.164.0 on the laptop, checks the committed dependency lock against every manifest module, verifies downloaded modules, and cross-compiles a static Linux executable with CGO disabled. It strips symbols, removes local paths/VCS metadata and clears the build ID. A caller's `GOOS`, `GOARCH`, `GOFLAGS` or workspace settings cannot accidentally make the generation step run a Linux executable on macOS.

Output: `dist/hostlier-collector-linux-arm64` or `dist/hostlier-collector-linux-amd64`. The script prints its SHA-256 and byte size. Generated sources and binaries stay under ignored `dist/`; the small manifest, scripts and exact `lock/go.mod` / `lock/go.sum` remain reviewable. This is an independent Go module and does not alter Hostlier's own dependencies.

## Package automatic installation assets

Run `./package-image.sh` to build both Linux architectures and write the deterministic OCI layout to `dist/collector-image/`. It uses the pinned upstream public CA bundle. From the Hostlier module directory, `make release` packages each controller executable alongside these assets in `dist/releases/hostlier-controller-linux-{arm64,amd64}.tar.gz`.

`hostlier init` installs these assets and the controller serves authenticated image pulls. Its runtime automatically maintains one shared collector per Docker host or Linux k3s node. This delivery path does not require publishing an image registry or preloading node caches. See [automatic installation](../docs/telemetry-collectors.md).

## Package a standalone local image

```sh
docker build --platform linux/arm64 -f Dockerfile.binary \
  -t hostlier-collector:0.164.0-minimal .
```

`Dockerfile.binary` copies the binary and the public CA bundle from the digest-pinned Contrib baseline into `scratch`. The full baseline image is only a build input: its executable, modules and other filesystem content do not enter the final image. This packaging route avoids recompilation inside a Linux VM and reuses an already-cached baseline image during comparisons.

For a complete containerized source build, the separate `Dockerfile` uses a digest-pinned Go 1.27.3 builder and copies the resulting binary and trust bundle into `scratch`:

```sh
docker build --platform linux/arm64 -t hostlier-collector:0.164.0-minimal .
```

Both images use `/hostlier-collector` as the entrypoint and default to `/etc/otelcol/collector.yaml`. They contain no shell or package manager. Root is retained because the shared collector reads host-owned log files; deployment manifests separately drop capabilities, disable privilege escalation and mount a read-only filesystem. Use the existing generated configuration, telemetry token Secret/environment reference and CA verification settings.

The image tag is local. Load it into the target Docker daemon or k3s containerd, or publish it to an appropriate registry yourself. The build does not push an image or claim a publicly published Hostlier registry artifact.

## Measurements

The initial Linux ARM64 build, using Go 1.26.10 for both comparison subjects:

| Executable | Bytes | MiB |
| --- | ---: | ---: |
| Official Contrib 0.164.0 | 391,250,082 | 373.13 |
| Hostlier minimal | 74,121,342 | 70.69 |

The minimal executable is **81.06% smaller**. Initial SHA-256: `4ef9911e34d7583be306c06593c1cdb5a0dbf3da05c4a634c968a8594874bd64`.

Binary size does not predict RAM or startup savings. Both distributions already activate only the components named in the configuration. Runtime comparisons must use the same workload, limits, configuration and compiler; see the [live comparison harness and results](../../benchmarks/collector-minimal/README.md). The minimal build has not become a 20 MiB agent merely by removing unused factories.

## Controller-served release assets

`./package-image.sh` builds both Linux architectures and writes `dist/collector-image/`, a deterministic OCI image layout containing a multi-platform index, per-platform manifests/configurations, and compressed scratch filesystems. Its public CA bundle is extracted from the digest-pinned baseline image. Packaging requires Docker only for that certificate extraction; all Go compilation runs on the laptop.

Extract the controller bundle with `collector-image/` beside `hostlier-controller`. The installer copies the referenced immutable blobs into its state directory and publishes `index.json` last. The controller then serves authenticated, read-only Registry API endpoints under its existing HTTPS origin. Nodes pull `CONTROLLER/hostlier/collector@sha256:INDEX_DIGEST`, selecting their Linux architecture automatically; there is no public registry publication or per-node manual image import.

The initial bundle includes ARM64 and AMD64, totals approximately 44 MiB compressed, and has index digest `sha256:b05b47171f698f4f3255f5105878f0eec121f1b3fe39a44021c8c7537ac9182c`. The AMD64 executable is 79,839,358 bytes, SHA-256 `48a9421ac988143f96ad7f67078cacc7e8d4b023de2b70a24df068ce57fc2f47`. The reader validates every referenced object's digest/size and refuses symlinks or unsupported platforms; release authenticity still depends on the installer's trusted bundle source.

## Deliberate dependency updates

Change `builder-config.yaml`, then refresh and review the lock explicitly:

```sh
# Run in this directory, with native host architecture settings.
unset GOOS GOARCH GOARM GOARM64 GOAMD64 CGO_ENABLED GOFLAGS
export GOTOOLCHAIN=go1.27.3 GOWORK=off
mkdir -p dist
go run go.opentelemetry.io/collector/cmd/builder@v0.164.0 \
  --config builder-config.yaml --skip-compilation
cp dist/generated/go.mod lock/go.mod
cp dist/generated/go.sum lock/go.sum
./build.sh arm64
```

Revalidate both runtime configurations and repeat the live fixtures after changing components, versions or the compiler. Ordinary builds fail if the manifest's direct module versions disagree with the committed lock.

Primary references: [OCB documentation](https://opentelemetry.io/docs/collector/extend/ocb/), [pinned Builder instructions](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.164.0/cmd/builder/README.md), and [upstream release manifest](https://github.com/open-telemetry/opentelemetry-collector-releases/blob/v0.164.0/distributions/otelcol-contrib/manifest.yaml).
