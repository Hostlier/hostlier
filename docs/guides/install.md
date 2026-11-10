# Install on your server

[All guides](../../GUIDE.md)

Start with an Ubuntu 24.04 or 26.04 LTS server. Download the controller bundle for its architecture, verify its checksum, and extract it. The directory must contain `hostlier-controller` and `collector-image/`. Install the separate CLI on your laptop. See [building a release](build.md#build-a-server-release).

Point the deployment domain and intended application hostnames at your server. The controller defaults to `hostlier.<domain>`. On the supported server, run the matching Linux executable:

```sh
sudo ./hostlier-controller init --domain apps.example.com --dry-run
sudo ./hostlier-controller init --domain apps.example.com
```

Initialization detects Docker and Nginx independently. Compatible existing Docker is reused; an existing Nginx installation selects coexistence automatically. Missing Docker is installed, and a host without Nginx uses Caddy by default. Existing apps, configuration and certificate renewal stay operator-managed. The preview explains each choice and all proposed changes before approval; `init --yes` permits explicit noninteractive installation approval. Unsupported or stopped existing services produce a preflight error instead of being replaced.

Use `--runtime k3s` to install k3s instead of Docker, even when Docker is already present. Proxy detection remains independent, so an existing host Nginx can also serve the new k3s workloads. `--proxy caddy`, `--proxy nginx` (fresh standalone Nginx), and `--proxy nginx-existing` remain explicit overrides. Re-running init without flags retains the recorded runtime, proxy and ownership; init does not switch an initialized runtime or proxy. Use the separate [Docker to k3s migration workflow](../runtime-migration.md) for reviewed application/data migration with backups and rollback. See [automatic detection and coexistence](../nginx-coexistence.md) for requirements and certificate reuse. Controller logs are written to `/var/log/hostlier/hostlier.log`; provider firewalls and DNS remain your responsibility.

The installer places `hostlier-controller` in `/usr/local/bin/` and starts
`hostlier.service` with `hostlier-controller run`. To use local socket
administration, install the optional Linux CLI separately:

```sh
sudo hostlier security passkey --name OWNER --socket /run/hostlier/controller.sock
```

The managed collector waits for the controller's HTTPS certificate and registry route
to be ready before starting its image download. During initial certificate issuance,
telemetry status remains `starting` with a readiness reason; provisioning proceeds
automatically once the check succeeds. Certificate verification remains enabled,
including when using a configured private CA or the dedicated fleet collector identity.

Install the matching macOS or Linux CLI on your laptop, then run
`hostlier connect https://hostlier.apps.example.com --name laptop`.
To upgrade an existing installation, extract the new controller archive and run
`sudo ./hostlier-controller init --dry-run`, then `sudo ./hostlier-controller init`. Do not use
an old installed controller to install the new release. On an existing installation,
`init` reads the saved application domain and controller hostname when those flags
are omitted; a missing or invalid saved domain requires an explicit `--domain` to repair.
With `nginx-existing`, the retry refreshes only Hostlier's owned controller route,
preserves deployed application routes and certificate references, validates the full
Nginx configuration, and gracefully reloads the existing master.
The retry also installs the dedicated certificate issuance unit needed when the
existing Certbot is a Snap package; simply replacing the binary leaves that unit
absent.

For published versioned releases, an installed controller can download and verify
the newest stable release from the official `Hostlier/hostlier` GitHub
repository. The bundle includes both `hostlier-controller` and `collector-image/`:

```sh
sudo hostlier-controller update --dry-run
sudo hostlier-controller update
```

The updater checks the fixed repository identity and release asset URLs, GitHub's
SHA-256 asset digests, and the matching `.sha256` file before extracting anything.
It then runs the downloaded controller's normal installer, which shows the plan
and asks for approval. `--yes` approves that displayed plan for automation.
The current hostname, runtime, proxy and application configuration are retained.
No release is available to this command until a version tag has completed the
controller build workflow and published a GitHub Release. Private repositories
require `GH_TOKEN` or `GITHUB_TOKEN` in the root process environment.
Install the first release containing `update` with the manual bundle procedure
above; subsequent releases can use this command.

Next: [Connect your laptop](connect.md) and [configure domains](domains.md).

## Add a box to a master controller

From a laptop connected to the master, add the box and select it for workload commands.

```sh
hostlier connect admin@box.example.com
hostlier status
```

`connect` selects the verified box for subsequent workload commands. To enroll it and keep your current selection, use `hostlier server add admin@box.example.com`. The box needs Ubuntu 24.04 or 26.04 LTS, and an SSH account with root or passwordless sudo. Point the controller and application DNS names at the box and allow web traffic on TCP 80 and 443. The master must be able to reach TCP 7464 on the box.

The CLI uses one SSH session to install the per-box controller and exchange public identities. Approve enrollment on the selected master. The CLI checks that the master can read the box’s state over mutually authenticated TLS. Workload commands subsequently use that connection. If the reachability check fails, correct the network path and rerun the command.

By default the first DNS label supplies the fleet name and the full DNS name supplies the controller hostname and application domain. Use `--name` for another fleet name, `--host` for the address the master can reach when the SSH destination is an alias, and `--domain` for another application domain. With a separate domain the controller hostname is `hostlier.DOMAIN`. For an IP address or short hostname, supply `--domain` or enter it at the interactive prompt.

A CI-built CLI uses the laptop’s authenticated GitHub CLI to fetch both Linux artifacts from successful builds of the same commit on main. For local builds, expired CI artifacts, or offline artifact transfer, put the matching Linux controller and CLI archives and their `.sha256` files in `~/.hostlier/releases/` (under `HOSTLIER_HOME` if set), or pass `--release-dir DIR`. The box reports its architecture during SSH setup and verifies both checksums before extraction.

### Connect an installed box, or switch its master

On the box, install the controller as usual, then give it the master's HTTPS URL.

```sh
sudo ./hostlier-controller init --domain apps.example.com
sudo hostlier-controller connect https://primary.example.com
```

Complete the master’s browser approval and fleet enrollment review. The command reads the box’s identity through its protected administration socket, derives the fleet address from its installed hostname, and checks the connection from the master. If the box is already installed, only the connect command is needed.

To move this box, run the same command with the new master. A profile saved in the invoking administrator's Hostlier home can also be used by name.

```sh
sudo hostlier-controller connect https://secondary.example.com
```

The old master continues to manage the box until the new master approves enrollment. The box then switches its pinned identity and checks the new master’s connection. If that check fails, the command attempts to restore the previous pin. A successful switch persists across restarts. The old master’s inventory entry remains until its administrator revokes it.

`hostlier server join MASTER` on the box is the equivalent CLI spelling. A saved master profile supplies authentication. For a new URL, the device credential is held in memory for the enrollment. Use `--host ADDRESS` only if the installed hostname is unreachable from the master, or `--name NAME` to resolve a fleet name collision. The explicit `init --master-fingerprint` and `hosts enroll` commands remain available for manual provisioning.

For subsequent updates and read-only migration previews using an existing CLI
login, see [controller maintenance](../controller-maintenance.md). Older
controllers require one initial administrator upgrade before these commands work.
