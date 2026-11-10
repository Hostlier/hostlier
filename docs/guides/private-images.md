# Private images

[All guides](../../GUIDE.md)

Both top-level `image: ghcr.io/your-org/your-app:latest` and version 2
`source.image` use **automatic local fallback**.
Hostlier detects the controller's architecture and asks the controller to download
first. If its registry access fails, the CLI downloads with your laptop's Docker
credentials and uploads automatically; a server registry login is optional.
See [deploy local containers from the CLI](container-deploy.md) for the full
configuration. Use `image` or `source.image` for registry references;
`source.container` accepts only local build contexts and image archives.
The rest of this guide explains the server registry login used by the first
download attempt. A server login is optional when the CLI can download using
your laptop's credentials instead.

For local fallback, log in on your laptop with
`docker login ghcr.io --username YOUR_GITHUB_USERNAME`, or
`docker login --username YOUR_DOCKER_USERNAME` for Docker Hub.

To let the controller download directly, log in to the registry **on your server**.
Hostlier reuses that account's existing Docker login for resolving image tags and
pulling images.
It does not run `docker login`, copy credentials into its state, or rewrite your
Docker configuration. This works on both Docker and k3s servers; see
[k3s servers](#k3s-servers) for how credentials reach Kubernetes.

## Optional server login

As the server account you use to install Hostlier:

```sh
docker login ghcr.io --username YOUR_GITHUB_USERNAME
# Or, for Docker Hub:
docker login --username YOUR_DOCKER_USERNAME
```

Enter your registry token at Docker's prompt. Then install Hostlier from the
extracted release bundle:

```sh
sudo ./hostlier-controller init --domain apps.example.com
```

Hostlier remembers the account that invoked `sudo`, rather than accidentally
using root's login. The setup preview shows the selected account and config path.
For a direct root installation, it uses root's login. `sudo docker login` writes
root's credentials, so use plain `docker login` when installing Hostlier with
`sudo` from an ordinary account. Docker login itself does not require access to
the Docker daemon.

On your connected laptop, use the private image in `.hostlier`:

```yaml
version: 1
name: prototype
image: ghcr.io/your-org/your-app:latest
port: 3000
route: prototype.apps.example.com
```

For a version 2 project, use `source.image`:

```yaml
version: 2
name: prototype
source:
  image: ghcr.io/your-org/your-app:latest
endpoint:
  exposure: public
  port: 3000
  route: prototype.apps.example.com
```

The [basic image example](../../examples/hello.hostlier) and
[sidecar example](../../examples/sidecars.hostlier) demonstrate both forms.

```sh
hostlier deploy
```

Normal deployment approval still applies. If the controller's registry access
fails, the CLI uses your laptop's Docker login and uploads the image. Credentials
remain on the machine that uses them.

## Existing installations

To configure a server login, install the updated release and rerun `init` as the
server user whose Docker login you want Hostlier to use. This records the login
owner and updates Hostlier's own
systemd service. Subsequent retries preserve that choice, even when run as root.
Restarting an old service unit alone does not grant it access to home directories.

For a custom Docker config directory, preserve it explicitly during installation:

```sh
sudo env DOCKER_CONFIG=/srv/my-docker-config ./hostlier-controller init --domain apps.example.com
```

Use a persistent directory, not `/tmp`. Without `DOCKER_CONFIG`, Hostlier uses the
selected account's `~/.docker/config.json`. It never searches other users' logins.

## k3s servers

CLI deployments prepare registry images as verified artifacts, then import them
into containerd on the k3s server. These artifacts do not require a registry pull
secret or a Docker daemon.

Direct controller API deployments that still reference remote images let
Kubernetes pull them. Hostlier supplies the server login through a pull secret:

- On each approved deployment, Hostlier reads the same server-account Docker
  login and writes a `kubernetes.io/dockerconfigjson` Secret named
  `hostlier-registry` in that app's own namespace. The Secret holds only the
  registries the app and its sidecars pull from. Pods reference it through
  `imagePullSecrets`.
- Each deployment refreshes the Secret, so a rotated token takes effect on the
  next `hostlier deploy`. If the app stops using private images, the next
  successful deployment removes the Secret, and deleting the app removes it too.
  A failed deployment never leaves behind a Secret the previous version didn't have.
- Public images get no Secret, and their manifests stay unchanged.
- Kubernetes needs a username plus a password or access token. Token-only logins,
  such as Docker identity or registry tokens, are rejected with a
  `REGISTRY_CREDENTIALS` error, never downgraded to anonymous pulls.

Unlike the Docker runtime, the credential now also lives in the cluster's own
datastore for as long as the Secret exists. It is scoped to the app's namespace,
which Hostlier creates and owns. Local artifact images are imported into
containerd and never use the Secret.

Existing k3s installations: install the updated release and rerun `init` as the
server user whose login you want to use (see below). Until then, the controller
can't read that user's home directory. CLI deployments can use local credential
fallback. Direct remote-image deployments can fail at tag resolution or rollout;
Hostlier rolls a failed rollout back.

## Credential helpers

Hostlier reads Docker's `auths`, registry-specific `credHelpers`, and global
`credsStore` settings. A registry-specific helper takes precedence over the global
store; a selected helper never falls back to stale inline credentials. Docker Hub
aliases, username/password or access-token logins, and Docker device-login identity
tokens are supported.

Helpers receive only Docker's `get` request for the target registry. They run as
the recorded user, with that user's home and standard executable paths, and have a
10-second timeout. Install helpers in `~/.local/bin`, `~/bin`, or a standard system
binary directory. Hostlier supplies the user's standard runtime directory and
D-Bus session address; it does not copy a terminal's arbitrary environment.

The Hostlier service mounts only the selected home, config directory, and user
runtime directory read-only; other homes remain hidden. A helper must work
noninteractively with its store already available and unlocked. Helpers needing
an interactive prompt, unavailable desktop session, custom environment, or writes
to those directories cannot complete under this service setup. Hostlier reports a
credential error instead of silently retrying with another login. It never invokes
helper `store` or `erase` operations.

Login changes are read on each lookup, so token rotation and logout take effect
without restarting Hostlier. They affect future registry requests; logout does not
stop running containers or remove already downloaded images. An already pinned or
locally cached image may skip remote tag resolution, but a pull always reads the
current login.

## Scope and verification

This integration applies to application images and sidecars on Docker and k3s.
Managed collectors have their own separate authentication.

Tests cover inline and helper credentials, helper precedence and failures, logout
and token rotation, authenticated tag resolution against a real TLS registry
fixture, Docker pull headers, identity-token exchange, and redirect/origin
isolation. k3s unit tests cover pull-secret contents (only the deployed
registries), Docker Hub key mapping, manifest ordering and Pod references,
anonymous images, and rejection of token-only or malformed logins. The k3s path
has not yet been run against a real cluster pulling from a private registry. A disposable Linux container also verified that helpers drop from root
to the selected user's UID. Race tests cover the affected Go packages.

A [live private GHCR test](../private-registry-validation.md) also verified a real
anonymous controller failure followed by automatic CLI download with local
credentials, a complete chunk upload, Docker deployment, matching image config
digests, and HTTP 200 through Caddy. It used Linux ARM64 containers on Docker
Desktop; installed-service, public HTTPS and private k3s rollout coverage remain
separate checks. The linked report includes the opt-in command for repeating it.

Docker protocol reference: [Docker login and credential helpers](https://docs.docker.com/reference/cli/docker/login/).
