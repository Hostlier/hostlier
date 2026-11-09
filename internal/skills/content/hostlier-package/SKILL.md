---
name: hostlier-package
description: Discover, install or inspect optional Hostlier packages locally or on a connected server, including verified GitHub release downloads.
---

# Hostlier package

Discover packages from a Git repository:

```sh
hostlier package available --repository https://git.example.com/team/packages.git
hostlier package available --repository git@git.example.com:team/packages.git --server production
hostlier package install postgres --repository https://git.example.com/team/packages.git --version COMMIT_FROM_LIST --server production
```

The repository must contain a regular root `hostlier-packages.json` catalog with schema `version: 1` and `packages`, each containing `name` and `artifacts`. Every artifact specifies `os`, `arch`, `sha256`, and exactly one relative tracked-file `path` or HTTPS `url`. Supported OS/architecture pairs are Linux or Darwin on amd64 or arm64. The SHA-256 must be 64 lowercase hex characters. Package names and platform selections must be unique. The CLI verifies actual binary bytes and platform before transfer.

Git discovery defaults to HEAD and returns its resolved commit as `version`. Installation requires an explicit tag, branch, or commit; prefer the commit returned by discovery. `package list --repository URL` is also supported. Git runs locally using configured credential helpers or SSH authentication; the server does not need Git. Discovery reads Git objects without checking out files, running scripts, building code or initializing submodules. A fetch may transfer tracked binary objects. Private artifacts can be stored in the Git repository; generic HTTPS downloads do not inherit Git credentials. The official GitHub source additionally supports scoped authentication for its own release asset API URLs.

The default source is the official Git catalog. Use `--github OWNER/REPO` explicitly to browse release assets:

```sh
hostlier package available
hostlier package available --github Usefused/hostlier-packages --version v0.1.0
hostlier package available --server production
hostlier package available --github OWNER/REPO
# Equivalent repository-list form:
hostlier package list --github Usefused/hostlier-packages
```

`available` defaults to the HEAD commit of the official Git catalog and returns that commit for installation. With `--github`, it lists release assets. `--server NAME` or `--socket PATH` filters the catalog to that controller's platform using a read-only query. Unqualified `package list` lists installed packages; supplying `--repository`, `--github`, or `--version` changes it to source discovery.

Discovery reads release metadata and, when needed, one small checksum manifest. It lists supported, uniquely named binary assets with usable integrity metadata; source archives and packages available only as source are omitted. It does not download or execute package binaries. Installation still verifies the actual bytes and native platform. Empty releases produce an empty package list; inaccessible repositories and missing releases produce errors.

Use an explicit server profile to install a package on a connected server:

```sh
hostlier package install postgres --github Usefused/hostlier-packages --version v0.1.0 --server production
hostlier package list --server production
hostlier package describe postgres --server production
```

The example version must exist as a published release. Use `--github` to select the release repository. Specify a release tag, not `latest`. The CLI discovers the server OS and architecture, downloads the matching `hostlier-NAME-OS-ARCH` release asset, and verifies its SHA-256 and native executable header. The release must provide GitHub SHA-256 metadata or a `SHA256SUMS` asset. It does not build arbitrary GitHub source code.

Git catalogs use the local Git credential configuration. Private GitHub release API requests use `GH_TOKEN`, then `GITHUB_TOKEN`, then a saved `gh auth login` session on the CLI machine. Credentials stay on that machine. The server requires neither GitHub access nor Go or Git to receive the binary.

Remote installation uploads nonexecutable chunks and requests the controller's normal exact hardware approval. The review binds the package name, checksum and claimed source coordinates. The controller rechecks the bytes and target platform before activation. A repository label is client-supplied provenance, not an independent publisher signature. `--yes` cannot bypass approval. Connect directly to the destination using `--server`; `--on` routing is not supported for package installation.

For locally built binaries:

```sh
hostlier package install postgres --file ./dist/hostlier-postgres-linux-arm64 --server production
sudo hostlier package install postgres --file /path/to/hostlier-postgres
sudo hostlier package list
```

For installation and installed-package inventory, omitting `--server` and `--socket` selects local filesystem administration, even when a default connection profile exists. Local storage defaults to `/var/lib/hostlier/packages`; use `--state-dir DIR` for another installation. Local installation requires filesystem authority and does not contact a controller. `--socket PATH` uses the controller's protected local administrative socket instead.

`describe` runs an installed package to read its identity and schemas. Installation alone does not provision a database or cloud resource. Optional package availability is distinct from live provider verification; do not infer that every advertised provider operation has been verified in production.
