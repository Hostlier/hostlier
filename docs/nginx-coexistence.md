# Automatic detection and Nginx coexistence

`hostlier-controller init` automatically detects Docker and host-installed Nginx independently. It reuses compatible existing components and installs only missing selected infrastructure:

| Existing setup | Default selection |
| --- | --- |
| Neither | Install Docker and Caddy |
| Docker only | Reuse Docker; install Caddy |
| Nginx only | Install Docker; reuse Nginx |
| Both | Reuse Docker and Nginx |

```sh
sudo hostlier-controller init --domain apps.example.com --dry-run
sudo hostlier-controller init --domain apps.example.com

# Explicitly use k3s; existing Docker is left alone.
sudo hostlier-controller init --domain apps.example.com --runtime k3s
```

`--runtime docker|k3s` and `--proxy caddy|nginx|nginx-existing` remain explicit overrides. Omitted flags or `auto` use detection. K3s selection does not disable Nginx detection: the existing host proxy can route to the new cluster's private services. This does not adopt an existing untracked k3s cluster.

Detection considers configuration files, sockets, binaries, packages and systemd units, including evidence of stopped installations. Compatibility is then checked against the actual running daemon and Nginx configuration. An incompatible or inactive existing component stops preflight; Hostlier never falls back to overwriting it. The approval preview records detected components and each resolved ownership choice. Repeated init preserves previously recorded choices, including legacy installations.
Use the normal release bundle, including `collector-image`. The controller hostname defaults to `hostlier.apps.example.com`. Configure its DNS and the new application hostnames before initialization or deployment. After initialization, pairing and application deployment work as usual.

## Ownership

Hostlier writes `/etc/nginx/conf.d/zz-hostlier.conf`, its private ownership record under `/etc/hostlier/nginx-existing`, and its own controller, telemetry and certificate-renewal assets. It tests the **complete** Nginx configuration and gracefully reloads the existing master when publishing its include.

It does not rewrite `nginx.conf`, existing site files, external Docker daemon configuration or containers, existing certificate files, external renewal jobs, or existing Docker/Nginx systemd units. Existing Docker and Nginx services are never restarted or enabled by installation. Missing selected runtimes are installed and started as Hostlier-managed infrastructure. Host firewall policy stays external when reusing Docker or Nginx; explicitly installing k3s also installs its own management firewall guard while retaining unrelated rules. Hostlier-created containers and volumes retain the existing ownership-label checks for lifecycle operations.

An existing site reserves its entire hostname. A new Hostlier app cannot take over that hostname or add a path underneath it. Exact and wildcard reservations are checked before creating a workload and again before publishing routes. An anchored regex `server_name` is permitted when its fixed DNS suffix proves it cannot match a Hostlier hostname. Regexes that may overlap, have alternation, or lack a fixed suffix, and dynamic `server_name` values, require separate review.

Do not edit the generated include manually. Hostlier detects changes to its recorded content and refuses to overwrite them. Keep manual sites in their own files. Unreadable configuration, unknown ownership and failed validation stop publication; failed activation restores the previous Hostlier include. Normal Nginx reload semantics apply: operator changes already saved in other included files are also loaded by a reload.

## Supported existing setup

- Ubuntu 24.04 or 26.04 LTS, amd64 or arm64, with systemd. Ubuntu 26.04 currently supports the Docker runtime; k3s remains limited to 24.04. Existing Docker and Nginx are reused when compatible. Missing packages use pins for the selected Ubuntu release.
- When reusing Docker: a running rootful Linux Docker Engine **28.x or 29.x** at `/var/run/docker.sock`. Docker's packaging source does not matter. Docker contexts, rootless Docker and remote engines are not supported in this mode.
- When reusing Nginx: a running host-installed `/usr/sbin/nginx` managed by `nginx.service`, using `/etc/nginx/nginx.conf` without a custom `-p` prefix. A unit may supply that same config path through `-c` or an expanded environment variable when the running master confirms it. Containerized Nginx is outside this first version.
- An existing `include /etc/nginx/conf.d/*.conf;` directly inside the main `http` block. Hostlier never inserts it into the user's configuration.
- An explicit `default_server` for existing wildcard IPv4 listeners on ports 80 and 443. This prevents a new include from changing the existing default site. Preflight reports incompatible layouts without changing them.
- Certbot already installed at `/usr/bin/certbot`. Hostlier does not install or upgrade existing Nginx/Certbot packages; missing selected runtimes and their dependencies are installed separately.

New Hostlier virtual hosts use IPv4 ports 80 and 443. DNS, public reachability, IPv6 routing, Nginx upgrades and host firewall policy remain operator responsibilities. Changing an already initialized Hostlier installation from another proxy or runtime remains unsupported.

## Certificates

Existing sites and their certificates continue using their original renewal setup. New Hostlier hostnames receive certificates through Certbot HTTP-01, with isolated configuration, work and log directories under `/etc/hostlier/nginx-existing-certificates`. The controller publishes an HTTP challenge route, then starts its exact-host `hostlier-nginx-existing-cert@HOST.service` job. This dedicated job supports Certbot installed through Snap while the long-running controller retains its service protections. `hostlier-nginx-existing-renew.timer` renews only those lineages; it never runs renewal against `/etc/letsencrypt`. HTTP challenges are scoped to Hostlier's named virtual hosts. Failed route stages are recorded by name in the root-only `/var/log/hostlier/hostlier.log`.

Optionally reference an existing certificate for a **new, unclaimed hostname** in `/etc/hostlier/nginx-existing-tls.json`:

```json
{
  "hostlier.apps.example.com": {
    "certificate": "/etc/letsencrypt/live/apps.example.com/fullchain.pem",
    "key": "/etc/letsencrypt/live/apps.example.com/privkey.pem"
  },
  "new-app.apps.example.com": {
    "certificate": "/etc/letsencrypt/live/apps.example.com/fullchain.pem",
    "key": "/etc/letsencrypt/live/apps.example.com/privkey.pem"
  }
}
```

Reference keys are exact hostnames; the same wildcard certificate may be referenced for several hosts. Hostlier checks hostname coverage, validity and the matching private key, and uses the paths directly. The existing renewal owner must continue renewing the certificate and reloading Nginx. Hostlier neither copies nor renews referenced keys. Public HTTPS checks still require a trusted certificate chain.

## Validation

Selection and command-audit tests cover all four presence combinations, explicit k3s, persisted ownership on retries, CLI forwarding and daemon/service noninterference. Unit tests cover service/package/firewall noninterference, hostname collisions, default-listener protection, configuration drift, validation/reload/issuance rollback, and external certificate references. The opt-in `TestLiveSharedNginxPreservesSitesAndTLS` test runs against a real Nginx master and verifies existing HTTPS traffic survives new app deployment, route removal and external certificate rotation.

A complete privileged Ubuntu coexistence rehearsal also passed: automatic reuse,
installed CLI deployment/restart/deletion, conflict rejection, repeated init, and
external certificate rotation, with 1,148 verified legacy HTTPS probes and zero
observed failures. See [results and limits](coexistence-validation.md) and the
[reproduction instructions](../test/e2e/VM_TESTS.md#existing-docker-and-nginx-coexistence).
Public Let's Encrypt issuance remains a separate gate; this rehearsal used a
trusted private CA and existing certificate references.
