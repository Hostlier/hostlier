# Browser and computer sidecar packages

The `sandbox-browser` and `sandbox-computer` packages live in the separate `hostlier-packages` source repository. They are local `project-contribute` packages: adding either to an existing v2 app proposes a sidecar, a parent URL binding, and a shared stored-token reference. They do not take over the app's primary build. Use Hostlier v0.1.5 or later for the CLI and controller; older CLIs reject sidecar contributions. Both packages are published in the default Git package catalog for macOS/Linux on AMD64/ARM64. Use `hostlier package available` to obtain the full catalog commit, then install with `hostlier package install sandbox-browser --version COMMIT` and `hostlier package install sandbox-computer --version COMMIT`; runtime container images remain separate builds.

`sandbox-browser` runs Chromium through Playwright 1.65.0. The parent connects using Playwright's `chromium.connect` WebSocket API with a bearer header. This is the Playwright protocol, not a CDP endpoint. Client and service must use matching Playwright major/minor versions.

`sandbox-computer` runs a Linux Xvfb/Openbox desktop with a terminal and a visible Chromium window. Its authenticated HTTP API supplies screenshots and bounded mouse, keyboard and scroll actions. It does not include a model or require an LLM provider account.

## Install and configure

In the package repository, build the two Go executables and their matching runtime images using `packages/sandboxes/README.md`. Install the package binaries on the CLI machine:

```sh
hostlier package install sandbox-browser --file /path/to/hostlier-packages/dist/hostlier-sandbox-browser
hostlier package install sandbox-computer --file /path/to/hostlier-packages/dist/hostlier-sandbox-computer
```

Create two stored secrets using `hostlier secret set browser-token` and `hostlier secret set computer-token`. Each value must contain 32–256 random URL-safe characters; a 32-byte random hex token is suitable. Keep values out of `.hostlier`.

From an existing app directory, add either or both packages:

```sh
hostlier package add sandbox-browser
hostlier package add sandbox-computer
```

Choose a built registry `image` or project-relative Docker image `archive` for each. For example, after saving the supplied images to the project:

```yaml
packages:
  sandbox-browser:
    archive: images/browser.tar
    tokenSecret: browser-token
  sandbox-computer:
    archive: images/computer.tar
    tokenSecret: computer-token
```

Each archive must contain the corresponding runtime built from the package repository; an arbitrary Playwright base image does not include the service or health probe. Omit the unused `image`/`archive` field. Run `hostlier plan`, then `hostlier deploy --server production` for the normal reviewed deployment. Do not use `--file` for package composition; that flag selects an already prepared deployment manifest.

The default member names are `browser` and `computer`; loopback ports are 9222 and 9333. Each receives 2 CPUs, 2 GiB RAM, a 512-process ceiling (shared across the Pod on k3s) and 1 GiB of temporary scratch space. Set `name`, `port`, `cpu`, `memory`, `urlEnv` or `tokenEnv` in the package settings to change those values. Declared port conflicts fail planning. Each sidecar is required for group readiness.

## Use from the parent app

The browser package supplies `BROWSER_URL` and `BROWSER_TOKEN`. Use a fresh browser context per task and close it afterward:

```js
import { chromium } from 'playwright'; // 1.65.x
const endpoint = new URL('/playwright', process.env.BROWSER_URL);
endpoint.protocol = 'ws:';
const browser = await chromium.connect(endpoint.toString(), {
  headers: { Authorization: `Bearer ${process.env.BROWSER_TOKEN}` },
});
try {
  const context = await browser.newContext();
  try {
    const page = await context.newPage();
    await page.goto('https://example.com');
    console.log(await page.title());
  } finally { await context.close(); }
} finally { await browser.close(); }
```

The computer package supplies `COMPUTER_URL` and `COMPUTER_TOKEN`. See the package README for the JSON action reference. Authenticated `GET /v1/info` describes its display dimensions, action names and API paths; `GET /v1/screenshot` returns PNG and `POST /v1/actions` performs one action. Both services have authenticated `GET /health` readiness probes. The desktop is shared by its callers; allocate separate app groups when agents need independent desktops.

## Isolation and lifetime

Both services bind to `127.0.0.1` inside the application's network namespace and have no public route. Tokens are scoped to the intended parent and sidecar. Containers run as UID/GID 1000 with a read-only root, dropped capabilities, no privilege escalation, no swap and bounded scratch space. Chromium's sandbox stays enabled with the pinned Playwright seccomp profile. There is no host socket, host directory or privileged container access.

Profiles, downloads and desktop files are ephemeral. Docker container replacement clears scratch data. On k3s, scratch is a separate memory `emptyDir` for each sandbox: it survives container restarts and is cleared when the Pod is replaced or deleted. Sidecars follow the parent's start/stop/restart/delete lifecycle and replicas; no separate sandbox session scheduler or idle timeout is added.

This is container isolation, not a VM or independent network boundary. The parent and its sidecars share loopback and outbound network access. Treat them as one trust group. Both Docker and k3s are supported. The packages select a generated OCI-compatible seccomp profile for the receiving server's AMD64 or ARM64 architecture, preserving the reviewed policy with all outer capabilities dropped. Hosts that prohibit the required unprivileged user namespaces fail browser startup rather than disabling Chromium's sandbox.

## k3s prerequisites

The same package settings, images, URL bindings and tokens work on Hostlier's managed single-node k3s runtime. Hostlier uses native restartable sidecars, authenticated exec startup/readiness probes, separate bounded memory scratch volumes and content-addressed `Localhost` seccomp profiles. It does not expose sandbox ports through Kubernetes Services.

A managed node security policy is required: a protected `k3s-security.json` in the controller state directory, a matching live kubelet Pod PID ceiling no larger than 512 (or any stricter secured member limit), `failSwapOn=true`, no active host swap and the standard `/var/lib/kubelet` root. The Pod PID budget covers the parent and all sidecars together. The [runtime migration workflow](../runtime-migration.md) prepares this contract when moving secured workloads from Docker. New k3s installations can have an administrator provision the policy before initialization; existing k3s nodes without it require administrator maintenance to configure and activate matching settings. A policy file alone does not satisfy the live verification.

The package repository's sandbox README includes node setup requirements and profile regeneration checks. Use Hostlier's pinned k3s version; native sidecar lifecycle requires Kubernetes 1.29 or later with `SidecarContainers` enabled. See Kubernetes documentation for [sidecar lifecycle](https://kubernetes.io/docs/concepts/workloads/pods/sidecar-containers/) and [node-local seccomp](https://kubernetes.io/docs/reference/node/seccomp/).
