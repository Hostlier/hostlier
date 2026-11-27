# Install on your server

[All guides](../../GUIDE.md)

Start with an Ubuntu 24.04 or 26.04 LTS server. Download the controller bundle for its architecture, verify its checksum, and extract it. The directory must contain `hostlier-controller` and `collector-image/`. Install the separate CLI on your laptop. See [building a release](build.md#build-a-server-release).

Point the deployment domain and intended application hostnames at your server. The controller defaults to `hostlier.<domain>`. On the supported server, run the matching Linux executable:

```sh
sudo ./hostlier-controller init --domain apps.example.com --dry-run
sudo ./hostlier-controller init --domain apps.example.com
```

New installations use Hostlier-managed K3s with its bundled containerd. Existing Docker installations or services on the host stay under their current owner. If a compatible Nginx installation is present, Hostlier reuses it. Otherwise the default is Caddy. The preview shows the runtime, proxy and proposed changes before approval. Use `init --yes` for noninteractive approval.

`--proxy caddy`, `--proxy nginx` (fresh standalone Nginx), and `--proxy nginx-existing` remain explicit overrides. Updates retain the recorded runtime, proxy and ownership, including Docker on existing Hostlier installations. To move Docker workloads and data to K3s, use the reviewed migration with backups and rollback. Controller logs are written to `/var/log/hostlier/hostlier.log`. Provider firewalls and DNS remain your responsibility. [Docker to K3s migration](../runtime-migration.md) · [Automatic detection and coexistence](../nginx-coexistence.md)

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

## Install the control plane on macOS

Use the macOS controller binary from the controller build artifacts. The control plane manages fleet enrollment, access, and approvals. Enrolled Linux workers run the workloads and own their application data. [Controller build artifacts](build.md)

Run these commands from your logged-in macOS account.

```sh
./hostlier-controller init --domain example.com --dry-run
./hostlier-controller init --domain example.com
```

Init creates the configuration and installs a per-user LaunchAgent. The controller starts immediately and at future logins. The API listens on loopback by default. Re-run init from a new macOS binary to update the agent while preserving the configuration and fleet identity.

For workers on other machines, configure a hostname they can reach and provide a TLS certificate and key for that hostname. Allow the workers to reach the HTTPS API on TCP 7463 and the mutually authenticated fleet listener on TCP 7464.

```sh
./hostlier-controller init --domain example.com \
  --controller-hostname control.example.com --host 0.0.0.0 \
  --tls-cert /path/to/fullchain.pem --tls-key /path/to/key.pem
```

By default the binary, state, admin socket, and logs are under `~/.hostlier/control-plane/`. The default LaunchAgent is `~/Library/LaunchAgents/com.hostlier.controller.plist`. Inspect the running agent with `launchctl print "gui/$(id -u)/com.hostlier.controller"`. With TLS terminated at a reverse proxy, omit the TLS options and use the default loopback API listener. Configure that proxy to forward native gRPC over HTTP/2.

On an installed worker, run `sudo hostlier-controller connect https://control.example.com:7463 --name worker-1`. On the Mac, approve the code printed by the worker.

```sh
hostlier-controller approve CODE --socket "$HOME/.hostlier/control-plane/controller.sock"
```

To enroll the first owner from the Mac, run `hostlier security passkey --name Owner --socket "$HOME/.hostlier/control-plane/controller.sock"` and open the HTTPS link it prints. Connect the Hostlier CLI to that HTTPS origin and approve the pairing. Select the worker with `--on worker-1` for deployment and workload commands. Swarms on a worker can be managed through that same connection.

## Add a box to a master controller

From a laptop connected to the master, add the box and select it for workload commands.

```sh
hostlier connect admin@box.example.com
hostlier status
```

`connect` selects the verified box for subsequent workload commands. To enroll it and keep your current selection, use `hostlier server add admin@box.example.com`. The box needs Ubuntu 24.04 or 26.04 LTS, and an SSH account with root or passwordless sudo. Point the controller and application DNS names at the box and allow web traffic on TCP 80 and 443. Each member controller opens a persistent, mutually authenticated gRPC connection to TCP 7464 on the master controller. Allow inbound TCP 7464 on the master and outbound access from the boxes; boxes do not need an inbound fleet firewall rule. Commands and watches share that connection, and disconnected boxes reconnect automatically.

The CLI uses one SSH session to install the per-box controller on K3s and exchange public identities. Previously installed controllers keep their recorded runtime. Approve enrollment on the selected master. The CLI waits for the member controller to connect outbound, then checks that the master controller can read its state over that session. Workload commands subsequently use that connection. If the reachability check fails, correct the network path and rerun the command.

By default the first DNS label supplies the fleet name and the full DNS name supplies the controller hostname and application domain. Use `--name` for another fleet name, `--host` for the box’s displayed address when the SSH destination is an alias, and `--domain` for another application domain. With a separate domain the controller hostname is `hostlier.DOMAIN`. For an IP address or short hostname, supply `--domain` or enter it at the interactive prompt.

A CI-built CLI uses the laptop’s authenticated GitHub CLI to fetch both Linux artifacts from successful builds of the same commit on main. For local builds, expired CI artifacts, or offline artifact transfer, put the matching Linux controller and CLI archives and their `.sha256` files in `~/.hostlier/releases/` (under `HOSTLIER_HOME` if set), or pass `--release-dir DIR`. The box reports its architecture during SSH setup and verifies both checksums before extraction.

### Connect a controller using only the terminal

After installing and starting the controller, run this on the **member controller**:

```sh
sudo hostlier-controller connect https://primary.example.com --name worker-1
```

The command prints an approval code and waits. On the **master controller**, run:

```sh
sudo hostlier-controller approve APPROVAL_CODE
```

Alternatively, the second command can run on your laptop using a saved login to the master:

```sh
hostlier server approve APPROVAL_CODE --server primary
```

That login must have `servers.add` permission or all access. The controller enforces this requirement, including expiry, revocation, and use limits; a deployment-only login cannot approve enrollment. The main CLI displays the same terminal confirmation without opening a browser. See [action permissions](controller-access.md#action-permissions) to grant access.

Review the member's name, reported hostname and fingerprint, then confirm in the terminal. Both commands select `/run/hostlier/controller.sock` automatically; `--socket PATH` is available for nonstandard installations. Neither command requires a browser, a saved profile, a hardware key, or the separate `hostlier` executable. `approve` requires an interactive administrator terminal and does not accept `--yes`.

The member's daemon signs its request with its existing fleet key. The initial exchange uses the master's public HTTPS endpoint and verifies its certificate; use `--ca-file FILE` (or `HOSTLIER_CA_FILE`) for a private CA. The master returns its fleet endpoint and pinned identity over that verified connection. Fingerprints are exchanged automatically, and enrollment grants no user or administrator credentials on the master. Requests expire after ten minutes and approvals apply to the exact signed member identity. Compare the approval code with the code printed on the intended member before confirming.

After approval the member saves the master selection and opens its outbound gRPC session. The command reports success only after that session is observed. Subsequent service or server restarts reconnect automatically using the saved state. Only the master needs inbound fleet access on TCP 7464; the member needs outbound access to the master's HTTPS API during enrollment and its fleet port for normal operation.

To change masters, repeat `connect` with the new master's HTTPS URL and approve there. The old master stays selected until approval. If the new outbound connection cannot be verified, the command attempts to restore the previous selection without overwriting a concurrent administrator's change. The old master's inventory entry remains until revoked.

`sudo hostlier server join HTTPS_MASTER --name worker-1` remains an alias when the optional CLI is installed; it uses the same browser-free protocol. Master profile names and the old browser-based join flow are no longer used for controller enrollment. The low-level `hosts identity`, `hosts enroll`, and `init --master-fingerprint --master-url` commands remain available for manual provisioning and recovery.

When upgrading an existing fleet, upgrade the master and members, then use the two-command flow to save each member's outbound master endpoint. A legacy pin-only configuration keeps local administration available but does not start a reverse session until it has a master URL. Requests to disconnected members return `HOST_UNAVAILABLE`; the fleet transport never automatically replays ambiguous mutations.

For subsequent updates and read-only migration previews using an existing CLI
login, see [controller maintenance](../controller-maintenance.md). Older
controllers require one initial administrator upgrade before these commands work.
