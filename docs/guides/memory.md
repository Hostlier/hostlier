# Memory controllers

A memory controller exports selected **existing** files, directories, or mounted
drives. A version 3 `.hostlier` file declares the paths and protocols. The
controller neither formats disks nor creates the selected source directories.

Agents can use S3 object operations or WebDAV; macOS and Linux can mount WebDAV
as a native network volume. Both interfaces read and write the same host files,
including images, documents, and code.

## Run a dedicated controller

The controller runs natively on **macOS (Apple Silicon and Intel)** and Linux.
macOS uses descriptor-relative filesystem operations and checks each component's
device and filesystem identity. Linux requires `openat2` and
`statx(STATX_MNT_ID)` support (Linux 5.8 or newer, with those calls allowed by the
service's sandbox). Serving fails closed on unsupported systems. Neither platform
needs a VM, Docker, or Kubernetes for memory mode.

Use a separate state directory and listener from any compute controller. A state
directory cannot be reused between controller modes. The existing compute
installer, `hostlier-controller init`, does not install memory mode; deploy the
controller binary as a service using `run --mode memory`.

Create a server configuration, for example `/etc/hostlier-memory/config.yaml`:

```yaml
version: 1
domains:
  - storage.example.com
```

Start the controller as an OS account with access to the selected existing data,
its private state directory, and its socket directory:

```sh
hostlier-controller run \
  --mode memory \
  --memory-root /srv/shared \
  --state-dir /var/lib/hostlier-memory \
  --config /etc/hostlier-memory/config.yaml \
  --hostname storage.example.com \
  --host 127.0.0.1 \
  --port 7465 \
  --socket /run/hostlier-memory/controller.sock
```

### Run on a MacBook

Use the native `hostlier-controller-darwin-arm64` binary on Apple Silicon, or
`hostlier-controller-darwin-amd64` on Intel, installed as `hostlier-controller`.
To build from this checkout, run
`go build -o hostlier-controller ./cmd/hostlier-controller`
and invoke `./hostlier-controller` instead.

macOS defaults to a private state directory at
`~/Library/Application Support/Hostlier/memory`, with `config.yaml` and
`controller.sock` inside it. A custom `--state-dir` changes both defaults;
`--config` and `--socket` can override them individually. No root account is
needed when your account owns the selected data and state directory.

Create the server configuration above at
`~/Library/Application Support/Hostlier/memory/config.yaml`, then select an
**existing** folder, for example:

```sh
hostlier-controller run \
  --mode memory \
  --memory-root "$HOME/Documents/Projects" \
  --hostname storage.example.com \
  --host 127.0.0.1 \
  --port 7465
```

An attached drive can instead use `--memory-root "/Volumes/My Drive"`.
macOS must permit the process to access that location. Keep the Mac awake and
the controller running while remote clients use the storage. This command runs
in the foreground; the Linux service installer does not install a Mac service.
The HTTPS reverse proxy and enrollment steps below apply on macOS too.

The `.hostlier` source paths must match the actual Mac paths, such as
`/Users/alex/Documents/Projects`, without shell variables or `~`. Paths must not
traverse symlinks; for example use `/private/tmp` instead of the `/tmp` alias.
For local administrator commands, pass
`--socket "$HOME/Library/Application Support/Hostlier/memory/controller.sock"`.

`--memory-root` is an administrator-set ceiling. Exports must be at or beneath
that existing directory, and cannot overlap controller state. A mounted drive
can itself be the ceiling, for example `/mnt/archive`. Use separate controller
instances for unrelated root directories. Changing a deployment cannot expand
this ceiling.

Put the loopback listener behind an HTTPS reverse proxy. Preserve the original
Host, Authorization, Destination, and If headers, URL paths and query strings;
allow WebDAV methods as well as GET/PUT/DELETE. CLI management uses gRPC, so the
upstream must support h2c. A Caddy site for an independently managed HTTPS edge:

```caddy
storage.example.com {
    reverse_proxy h2c://127.0.0.1:7465
}
```

Use [controller enrollment](controller-access.md) for an owner connection. When
bootstrapping through the local administrator socket, pass the memory
controller's socket explicitly. The socket belongs only to the trusted service
account/administrator. Run the service with the minimum host filesystem
permissions needed; do not give agents shell access to its OS account.

Memory mode exposes storage, enrollment, approval, and storage lifecycle APIs.
It rejects compute deployments, packages, resources, fleet operations, tunnels,
and workload recovery APIs. There is no Docker or Kubernetes dependency.

## Define and deploy exports

Paths are interpreted on the controller, never on the CLI's laptop. All sources
must already exist. For a controller whose root is `/srv/shared`:

```yaml
version: 3
kind: memory
name: library
exports:
  workspace:
    path: /srv/shared/projects
    protocols: [webdav, s3]
    readOnly: false
  reference:
    path: /srv/shared/reference.pdf
    protocols: [webdav]
    readOnly: true
```

`readOnly` defaults to false in the export definition. Each issued credential
has its own access setting; the API defaults credentials to read-only and the
CLI requires explicit `--allow read` or `--allow write`. An export's read-only
setting always wins.

```sh
hostlier memory validate --file .hostlier
hostlier memory plan --file .hostlier --server storage
hostlier memory deploy --file .hostlier --server storage
hostlier memory list --server storage
```

Planning records the source's device, inode and mount identity. Applying the
approved plan rechecks them. Remote deployment and grant issuance use Hostlier's
existing owner approval flow. Restricted application deployment credentials
cannot export host storage.

An individual file appears as a directory containing only its original basename.
It can be read or updated in place if writable; clients cannot add siblings,
rename it, or delete it. Editors that save using a temporary sibling and rename
need a directory export instead.

## Give an agent or native client access

```sh
hostlier memory grant library workspace \
  --allow write --for 24h --output workspace-grant.json --server storage
```

The CLI exclusively creates a mode-0600 file, never overwrites an existing file,
and does not print its secret. It includes a WebDAV username/password, S3 access
key/secret, endpoint paths, bucket name, region and expiry. The same credential
works only for this export and only for its enabled protocols. It cannot call
controller management APIs or choose arbitrary host paths.

For native mounting, first create an empty local mountpoint:

```sh
mkdir -p "$HOME/Hostlier/library"
hostlier memory mount library workspace \
  --output "$HOME/Hostlier/library" --server storage
```

Enter the username and password from the grant file when the operating system
prompts. Credentials are not passed as process arguments. macOS uses
`mount_webdav`; Linux uses `mount -t davfs` and requires davfs2 and permission
to mount. Use `umount` on that mountpoint to disconnect.

In Finder, **Go → Connect to Server** accepts:

```text
https://storage.example.com/memory/webdav/library/workspace/
```

Linux file managers with WebDAV support can use the equivalent secure WebDAV
location (often `davs://storage.example.com/memory/webdav/library/workspace/`).
The OS must trust the server's TLS certificate. Hostlier's management trust
configuration does not automatically install a private CA into Finder or davfs2.

For S3 SDKs, use:

| Setting | Value for this example |
| --- | --- |
| Endpoint | `https://storage.example.com/memory/s3/library` |
| Bucket | `workspace` |
| Region | `us-east-1` |
| Addressing | Path style |
| Credentials | `accessKeyId` and `secretAccessKey` from the grant |
| Signing | AWS Signature Version 4, Authorization header |
| Extra checksum calculation | When required |

Use single-request PutObject, not an SDK multipart upload manager. For example,
with the Go AWS SDK already used by Hostlier:

```go
storage := s3.New(s3.Options{
    BaseEndpoint: aws.String("https://storage.example.com/memory/s3/library"),
    Region: "us-east-1",
    UsePathStyle: true,
    Credentials: credentials.NewStaticCredentialsProvider(accessKey, secret, ""),
    RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
    ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
})
```

Supported operations are HeadBucket, GetBucketLocation, ListObjectsV2,
GetObject/HeadObject (including byte ranges), PutObject, and DeleteObject.
Keys represent filesystem-relative paths; empty components, `.`, `..`, backslashes
and directory-marker objects are not supported. Uploads are limited to 1 GiB;
listing is bounded to 10,000 visible entries per export. ETags are SHA-256 values.

This is a deliberately limited filesystem-backed S3 interface. Multipart
uploads, CopyObject, presigned URLs, object metadata, ACLs, tagging, conditional
writes, additional checksum headers, versioning and bucket administration are
not supported. Unsupported operations are rejected rather than silently applied.
Native WebDAV locks also block S3 writes to the locked object.

Revoke access with:

```sh
hostlier memory grants library --server storage
hostlier memory revoke library GRANT_ID --server storage
```

Credentials expire after 1 minute to 30 days. Revocation rejects subsequent
requests; it does not undo or necessarily cancel a request already admitted.
Redeploying the library invalidates its prior credentials. Ordinary process
restarts retain encrypted credentials and reopen matching sources.

## Isolation and storage semantics

Every exported operation resolves beneath a pinned source descriptor using
kernel-enforced no-follow operations. Requests cannot follow symlinks (including
links that point back inside the export), magic links, or nested mounts.
Multiply-linked regular files and devices, sockets and FIFOs are rejected and
omitted from listings. Select a nested mounted drive as its own export if it
needs access.

On macOS, component-by-component `openat` resolution rejects symbolic links,
checks filesystem boundaries, and verifies opened file identities before
truncation. State exclusion also compares filesystem identities, preventing
case-insensitive names, Unicode aliases, or firmlinks from exposing private
controller state.

Replacing or moving the source, unmounting its drive, or changing its mount
identity makes the export unavailable. Remounts, reboot, or service mount
namespace changes may require a new reviewed deployment. The controller must
never silently export the empty directory beneath a disconnected drive.

This boundary protects against storage clients. The host administrator, kernel,
and local processes with permission to move or mutate the exported tree remain
trusted. Do not allow hostile local processes to reorganize export roots or
create links concurrently. Native local edits are visible, but they do not
participate in remote locking or serialization.

This is live shared storage. Remote requests are serialized across exports,
and WebDAV supports locks. Locks are in memory and are lost on redeployment or
restart. Overlapping exports have separate lock namespaces, so avoid overlapping
writable exports when relying on editor locks. Unlocked edits use last-writer
semantics. There are no branches, snapshots, automatic consolidation, offline
sync, or arbitrary-file merge features in this mode.

S3 stages and validates uploads before writing the selected file. Filesystem
write failures can still leave partial data; WebDAV writes stream directly to
the file. External local processes may observe a write in progress. Keep normal
backups of important data; controller-state backups do not back up export data.
There is no storage quota management in this release.

Native mounts provide network-drive file operations, not all local POSIX
filesystem features. In particular, arbitrary modification times set through
Finder's WebDAV client or davfs2 were not preserved after remounting. Extended
attributes, executable permissions, and application-specific metadata should not
be assumed to behave like a local APFS or ext4 volume.

`hostlier memory stop library` disables serving, `start` rechecks the source,
and `delete` removes the deployment while retaining host files. Purging source
data is forbidden. SMB is not implemented.

## Validation

Linux and macOS tests exercise traversal, symlink swapping, hard links, FIFOs,
source replacement, single-file restrictions,
WebDAV locks and mutations, S3/WebDAV interoperability, signed AWS SDK requests,
credential scope, expiry, revocation, restart recovery and deployment removal.
CLI tests cover offline validation, plan routing and private credential files.

Platform tests cover nested mounts and detached drives. macOS additionally
checks case/Unicode aliases of private state. On macOS, set
`HOSTLIER_TEST_DISK_IMAGE=1` to run a temporary disk-image eject test, or
`HOSTLIER_TEST_NATIVE_MOUNT=1` for the real `mount_webdav` client test:

```sh
HOSTLIER_TEST_DISK_IMAGE=1 HOSTLIER_TEST_NATIVE_MOUNT=1 \
  go test ./internal/memory -run Memory -count=1
```

These opt-in tests clean up their temporary mounts and data. Finder against a
Linux controller and Linux davfs2 were also exercised manually for folder
creation, copy, rename, deletion, Unicode names, binary data, atomic editor
saves, and content persistence after remount. These checks do not substitute for
testing every native editor or filesystem feature.
