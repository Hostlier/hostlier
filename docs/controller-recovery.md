# Controller metadata recovery

The controller stores its aggregate state in `state.db` using SQLite transactions with FULL synchronization. Workflow records and the event cursor commit together. On first open, a legacy `state.json` is authenticated and migrated as one snapshot. Keep the original `secret.key`: losing it makes encrypted credentials and workflow results unrecoverable. One controller process exclusively owns a state directory.

Stop the source controller before backing up. For the Caddy edge, also stop `caddy.service` while collecting its ACME account and certificate state so it cannot renew or rewrite files during the snapshot:

```sh
sudo systemctl stop hostlier
sudo hostlier-controller backup --state-dir /var/lib/hostlier --output /secure/controller.tar
```

The owner-only checksummed archive includes a consistent SQLite snapshot, the original encryption key, packages and adapter metadata, installed configuration, Nginx certificate material under `/etc/hostlier`, and Caddy ACME accounts and certificates under `/var/lib/caddy`. Treat it as a secret. It excludes live database contents and application volumes; back those up separately with the engine or storage provider. Public ACME account state and certificates require the same care as other credentials.

Restore requires an absent destination and an explicit acknowledgement that the source is stopped:

```sh
sudo hostlier-controller restore --source-stopped --input /secure/controller.tar --state-dir /var/lib/hostlier
```

Restore validates archive paths, file types and hashes before publishing the directory. It preserves resource ownership and encrypted bytes. A different destination may be used for staging and inspection, but runtime activation and re-export are fenced until the metadata is at the exact original absolute state-directory path. Do not run source and recovered controllers concurrently.

Installed settings and TLS files are staged under `recovery-config/etc/hostlier`; the selected config is also retained as `recovered-config.yaml`. Review and reinstall these at their original system paths with protected permissions before starting the controller. Certbot links are recreated only when confined within that staged configuration tree. Ephemeral Nginx PID files are excluded. Caddy state is staged under `recovery-config/var/lib/caddy`; reinstall it at `/var/lib/caddy` with owner `caddy:caddy`, private directories (`0700`) and files (`0600`) before restarting Caddy. Restore stages all private files as the restoring user, so it does not assign the Caddy service account automatically. Subsequent backups skip the old `recovery-config` staging tree and collect the current system settings and edge state afresh. Restore does not install packages, start services, move application data, or claim unmanaged resources. Reinstall the same runtime and selected edge, restore their data separately, then inspect configuration and routes before enabling services.

Nginx initialization owns a standalone configuration and service. Repeated initialization preserves active routes and verifies configuration before reload. Its renewal timer runs Certbot twice daily and reloads only after `nginx -t` succeeds. Caddy remains the default; changing an installed edge requires an explicit migration outside initialization.

The Nginx installation gate has been exercised on an Ubuntu 24.04 arm64 Lima VM with Docker: fresh initialization, a certificate-valid application deployment, repeated initialization retaining the application route, and reboot persistence. Those installation tests preseed and trust local self-signed certificate fixtures. A separate real local ACME issuance/renewal gate is described below; public ACME issuance is not claimed. The separate live Nginx gate covers TLS, path boundaries, SSE, WebSocket upgrades, rejected configuration, certificate replacement, and scale-to-zero HTTP 503. The fresh bootstrap waits for the asynchronous HTTPS listener after reload before marking installation ready.

## Fresh-server data recovery

The local rehearsal uses a newly created Ubuntu VM, never a VM snapshot or copy of `/var/lib/docker`. Keep the source VM stopped throughout destination activation. Preserve the same Hostlier version, architecture, runtime, edge, and absolute StateDir (`/var/lib/hostlier` in the fixture).

1. Stop application writes with `hostlier stop APP`. Run `hostlier resource backup RESOURCE --output /secure/resource.native` before stopping the controller. Keep the returned backup ID: the controller stores its native payload encrypted alongside its metadata. Independently archive each application volume while its writer is stopped, recording the exact volume name and `io.hostlier.*` labels. A native database export and an application-volume archive are separate from the controller metadata archive.
2. Stop the controller and selected edge/renewal services, run the controller backup command above, and keep the source server stopped. Transfer all archives privately. The controller bundle is not a database/volume filesystem snapshot.
3. On the fresh server, install the same supported runtime, edge and binary. Restore controller metadata into the absent **original absolute** state directory. Review and reinstall staged `/etc/hostlier` settings and matching systemd units, reinstall private CA trust if applicable, and recreate the challenge directory. Enable the selected edge and controller services only after these protected assets are present. Do not start a second controller against a running source.
4. Recreate each application volume with its recorded exact name and ownership labels, then unpack its separate trusted backup into the new volume before application deployment. Never attach unrelated or unlabeled existing volumes. Restore database data through an approved `hostlier resource restore TYPE RECOVERED_NAME --storage SIZE --backup BACKUP_ID`; this creates an independently authenticated instance. The retained encrypted backup ID from step 1 works after controller recovery, without uploading raw native files to a new API.
5. Review a project referencing the restored database instance and the original application/volume identities, then approve its normal deployment plan. A restored controller at the original StateDir may replace an old writer only when Docker authoritatively reports the old container missing (HTTP 404). Ownership mismatches and all other daemon errors remain failures. This exception applies only to the local deployment attempt; retained desired state is not erased before the candidate activates.
6. Verify certificate-valid application HTTP, application reads of the restored native database and volume, expected secret bindings, private resource networking, package executability and owner-only key files. Reboot and repeat the checks before retiring source backups.

`test/e2e/recovery_vm.py` records this procedure as `setup`/`backup` on the disposable `nginx.example.test` source and `restore`/`activate`/`check` on a different fresh VM. It requires root, no destination state or containers before restore, and a separately installed runtime/edge. The fixture carries reviewed service files and test CA certificates with the independent archives. `check` can be repeated after reboot. Its Python HTTP app returns only fixed volume/database markers and a boolean for secret availability; it never returns credentials. Production recovery needs the equivalent reviewed system paths and native data procedure for the chosen engine.

## Real local ACME gate

`test/e2e/acme_vm.py` runs pinned Pebble 2.8 with real HTTP-01 validation and the compiled `internal/nginx` test binary inside a disposable Linux VM. It proves missing-certificate issuance, actual Certbot renewal, the generated renewal service's `nginx -t && nginx -s reload` hook, a changed certificate serial, trusted hostname verification, and application HTTP before/after renewal. The test trusts explicit fixture CAs; it never uses an insecure TLS flag or Pebble's always-valid mode. An optional `nginx.Options.ACMEDirectoryURL` accepts only HTTPS numeric loopback URLs for this integration test. Installed production defaults and trust behavior are unchanged. This is local ACME interoperability evidence, not a claim of public-issuer or public-DNS validation.
