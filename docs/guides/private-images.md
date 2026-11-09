# Private images

[All guides](../../GUIDE.md)

Log in to the registry **on your server**, then deploy normally. Hostlier reuses
that account's existing Docker login for resolving image tags and pulling images.
It does not run `docker login`, copy credentials into its state, or rewrite your
Docker configuration. This works on both Docker and k3s servers; see
[k3s servers](#k3s-servers) for how credentials reach Kubernetes.

## Log in once

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
version: 2
name: prototype
source:
  image: ghcr.io/example/prototype:latest
endpoint:
  exposure: public
  port: 3000
  route: https://prototype.apps.example.com
```

```sh
hostlier app deploy
```

Normal deployment approval still applies. A login on your laptop alone does not
authenticate the server.

## Existing installations

Install the updated release and rerun `init` as the server user whose Docker login
you want Hostlier to use. This records the login owner and updates Hostlier's own
systemd service. Subsequent retries preserve that choice, even when run as root.
Restarting an old service unit alone does not grant it access to home directories.

For a custom Docker config directory, preserve it explicitly during installation:

```sh
sudo env DOCKER_CONFIG=/srv/my-docker-config ./hostlier-controller init --domain apps.example.com
```

Use a persistent directory, not `/tmp`. Without `DOCKER_CONFIG`, Hostlier uses the
selected account's `~/.docker/config.json`. It never searches other users' logins.

## k3s servers

Kubernetes pulls images itself, so on a k3s server Hostlier hands it the login
through a pull secret:

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
can't read that user's home directory. Private images then fail at tag resolution,
or at rollout for digest-pinned images, and Hostlier rolls the deployment back.

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
to the selected user's UID. Race tests cover the affected Go packages. No real
Docker Hub or GHCR account was used in these tests.

Docker protocol reference: [Docker login and credential helpers](https://docs.docker.com/reference/cli/docker/login/).
