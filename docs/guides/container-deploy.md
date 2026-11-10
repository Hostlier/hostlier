# Deploy local containers from the CLI

Hostlier can prepare a registry image with automatic local fallback, build a local
Docker context, or upload a prebuilt Docker image archive.
The CLI verifies the image, sends resumable chunks over its saved HTTPS connection,
and submits the resulting immutable image to ordinary planning and approval. No
registry publication, staging script, SSH session or remote Docker build is needed.

```yaml
version: 2
name: my-minion
source:
  container:
    context: ./dist/my-build
endpoint:
  exposure: public
  port: 8080
  route: https://my-minion.example.com
health:
  path: /healthz
```

The context must contain a `Dockerfile`, or select a recipe inside that context with `source.container.dockerfile` (for example `.hostlier-build/web.Dockerfile`). This supports a shared monorepo context with a separate recipe for each app. Absolute paths, traversal outside the context, and escaping recipe symlinks are rejected. `plan`, `deploy`, and `update` explicitly
run that Dockerfile locally through Docker Buildx, for the receiving controller's
Linux CPU architecture. Docker must be running. The Docker build cache avoids unchanged
build steps; credentials follow ordinary local Docker registry authentication.
Use `.dockerignore` to keep secrets and unrelated files out of the build context.
Runtime secrets belong in Hostlier's `secrets` references, not the Docker build.

To deploy a registry image with automatic download and credential fallback,
use `source.image`:

```yaml
source:
  image: ghcr.io/your-org/your-app:latest
```

Version 1 projects use the same reference in top-level `image`. Architecture
discovery, controller-first download and automatic CLI fallback work identically.

Use `hostlier plan`, `hostlier deploy`, or `hostlier update` as usual. The CLI first
discovers the selected controller's Linux image architecture, including when using
`--on HOST`. It asks that controller to download and stage the full image using
its own registry login, or anonymous access for public images. This preparation
creates no containers or Pods and does not deploy the app.

If the controller cannot reach or authenticate to the registry, the CLI downloads
using your local Docker config (`DOCKER_CONFIG` or `~/.docker/config.json`),
including credential helpers, and uploads the verified image. For this fallback,
log in on your laptop with `docker login ghcr.io --username YOUR_GITHUB_USERNAME`.
Docker Hub images such as `your-org/your-app:latest` work the same way after local
`docker login`. Neither registry-download path needs a Docker daemon. Credentials
stay on the machine that uses them; laptop credentials are never sent to the
controller.

Tags and digest-pinned images are supported. A multi-platform image selects the
controller's Linux architecture, and single-platform images must match it. The
same automatic architecture discovery applies to `context` and `archive`
sources. You may still set `architecture: amd64` or `arm64` as an explicit
constraint; a mismatch stops preparation. Controller authentication, platform,
disk-space and controller-connection errors stop the command instead of causing
a local download.

The downloaded image follows the same verified, resumable upload and deployment
approval flow as an archive. Each preparation resolves and downloads the selected
image again, while identical committed content is reused. This also
works for sidecars. `hostlier validate` checks the reference without contacting a
registry or invoking credential helpers.

Both CLI and controller must include this preparation support. The controller
keeps at most two registry preparations active at once, leaves its filesystem
reserve intact, and removes abandoned temporary download directories after the
normal cleanup grace period when no download is active.

Choose `image` or `source.image` for registry references. `source.container` is
only for local inputs: select exactly one of `context` or `archive`; `dockerfile`
is only valid with `context`.
Already prepared `hostlier.local/artifacts@sha256:...` references are
reused without another registry download, including in version 1 projects.

For a prebuilt archive, replace `context` with `archive: ./image.tar` produced by
`docker save`. The archive must contain one Linux image matching the receiving
controller (`amd64` or `arm64`). Input archives and prepared images have no fixed byte cap;
uploads use 1 MiB chunks and require enough free space on the controller. Archive preparation does not execute application code. Container sources
also work for sidecars and retain their own entrypoint; they are not Harnest profiles.

Paths are relative to the selected project file. `hostlier validate` inspects local
inputs and configuration without building, uploading, or contacting the controller.
Controllers receive only the verified immutable image, never local paths or Dockerfiles.
Interrupted transfers verify retained prefixes before resuming. Identical committed
content is reused. Existing server cleanup and retention rules apply.

Select a saved connection once, then use ordinary commands:

```sh
hostlier server use production
hostlier validate
hostlier plan
hostlier deploy
hostlier logs my-minion --tail 30
```

An explicit `--server NAME` overrides the saved default. `--socket` is for local
server administration and is not part of the remote deployment workflow.

Secret provisioning also uses the CLI: `hostlier secret set NAME` reads a hidden
value and submits it over HTTPS. It requires an owner-authorized connection and
the existing approval flow. A scoped deployment credential can use allowed secret
references but cannot create or change secrets. Expired or app-restricted grants
must be renewed with the correct app/secret scope; the CLI never broadens them to
make a deployment succeed. See [controller access](controller-access.md).
