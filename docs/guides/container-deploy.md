# Deploy local containers from the CLI

Hostlier can build a local Docker context or upload a prebuilt Docker image archive.
The CLI verifies the image, sends resumable chunks over its saved HTTPS connection,
and submits the resulting immutable image to ordinary planning and approval. No
registry publication, staging script, SSH session or remote Docker build is needed.

```yaml
version: 2
name: my-minion
source:
  container:
    context: ./dist/my-build
    architecture: amd64
endpoint:
  exposure: public
  port: 8080
  route: https://my-minion.example.com
health:
  path: /healthz
```

The context must contain a `Dockerfile`, or select a recipe inside that context with `source.container.dockerfile` (for example `.hostlier-build/web.Dockerfile`). This supports a shared monorepo context with a separate recipe for each app. Absolute paths, traversal outside the context, and escaping recipe symlinks are rejected. `plan`, `deploy`, and `update` explicitly
run that Dockerfile locally through Docker Buildx, for the selected Linux CPU
architecture. Docker must be running. The Docker build cache avoids unchanged
build steps; credentials follow ordinary local Docker registry authentication.
Use `.dockerignore` to keep secrets and unrelated files out of the build context.
Runtime secrets belong in Hostlier's `secrets` references, not the Docker build.

For a prebuilt image, replace `context` with `archive: ./image.tar` produced by
`docker save`. The archive must contain one Linux image matching `architecture`
(`amd64` or `arm64`). Input archives and prepared images have no fixed byte cap;
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
