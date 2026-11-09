# Build and test Hostlier

[All guides](../../GUIDE.md)

Use Go 1.27.3 or newer. Node.js is not required to build the CLI from the checked-in UI assets.

```sh
cd hostlier
make build
./dist/hostlier help
./dist/hostlier-controller help
make test
make check
make linux
```

`make quality` enforces cyclomatic complexity ≤10 for all Go functions and closures, including tests, and requires comments on functions, types and control-flow statements. `make check` runs that gate, `go vet` and the race suite. See [QUALITY_REVIEW.md](../../QUALITY_REVIEW.md) for the refactor and verification evidence.

`make build` produces two independent executables: `dist/hostlier` for the CLI and local dashboard, and `dist/hostlier-controller` for server installation, daemon startup, fleet execution, and offline recovery. Use `make cli` or `make controller` to build only one. The CLI does not link the controller, runtime adapters, SQLite state store, S3 recovery SDK, or embedded NATS server; the controller does not link the CLI, dashboard assets, or skills.

`make linux` produces both static executables for Linux amd64 and arm64 in `dist/`. The CLI also builds natively on macOS. Installation targets supported Ubuntu servers with systemd; it does not install server infrastructure on macOS. Tests that bind local sockets need permission to create listeners.

## Build a server release

From the Hostlier source directory, run `make release` to build Linux amd64 and
arm64 archives under `dist/releases/`. Each archive includes the `hostlier-controller`
executable and its sibling `collector-image/` directory; copy and extract both on
the server. Collector packaging prerequisites are in [collector/README.md](../../collector/README.md).
Use `make build` for the CLI on your laptop, then put `dist/hostlier` on your PATH.

Next: [Install on your server](install.md).

## Optional package source

The standalone package repository is [Usefused/hostlier-packages](https://github.com/Usefused/hostlier-packages), currently private. It builds without the controller or CLI checkout and includes a package SDK, database/provider adapters, tests, and editable sample workflows. Its README documents `make build`, `make linux`, and the separate Pulumi module.

The original package copies in this checkout remain for compatibility with existing build and integration-test paths during migration. Author new package changes in the package repository. Its `examples/` directory contains sample workflows to adapt; it is not a hosted product/template registry.

To verify freshly built package binaries against this checkout's actual resource and workflow clients:

```sh
HOSTLIER_PACKAGES_DIR=/absolute/path/to/hostlier-packages go test -race ./internal/resourceclient -run TestExternalPackageRepository
```

Run `make build` in the package repository first. This check uses temporary local installations and read-only plans; it does not provision databases.

## Install the package SDK and remote packages

`hostlier sdk install --version TAG_OR_COMMIT --directory GO_PROJECT` adds the standalone Go package-authoring SDK from `Usefused/hostlier-packages` to an existing local Go module. It resolves the ref to a commit before invoking Go. This SDK is distinct from the broader hosting-sdk project.

`hostlier package available` lists names and platforms from the official Git catalog and returns the commit to use for installation. Use `--server production` to filter by server platform. `hostlier package list` shows installed packages.

`hostlier package install postgres --version COMMIT_FROM_LIST --server production` verifies the matching artifact and sends it to the connected controller for approval. Install the Minion or Harnest project adapter on the CLI machine by omitting `--server`. To explicitly install from a GitHub release, use `--github OWNER/REPO --version RELEASE_TAG`. A local binary can be installed with `--file BINARY`.

Git fetches the catalog with the CLI machine’s Git credentials. GitHub release API access uses local `GH_TOKEN`, `GITHUB_TOKEN`, or a saved `gh auth` session. The controller receives verified binary bytes and public source coordinates; it does not need GitHub credentials, Go, or Git. Releases must contain `hostlier-NAME-OS-ARCH` assets and SHA-256 metadata or `SHA256SUMS`. Run `hostlier help package` and `hostlier help sdk` for complete usage.


## GitHub Actions builds

Two independent workflows run on pull requests to `main`, pushes to `main`,
and manual **Run workflow** requests:

- **Build Hostlier controller** handles controller, installer, runtime, collector
  assets and controller packaging changes. It builds the two Linux server bundles.
- **Build Hostlier CLI** handles the client, local dashboard, bundled skills and
  CLI packaging changes. It builds the two Linux CLI archives.

Shared packages, Go module files, the Makefile, boundary checks and integration
tests trigger both. Documentation-only changes outside embedded skills do not
trigger bundles. Each workflow's own configuration triggers only that workflow.
New shared package directories conservatively trigger both until classified.

Each workflow verifies Go modules, checks executable separation, runs `go vet`,
and runs the full race-enabled test suite before packaging. Builds use Go 1.27.3
and the checked-in UI assets.

Download these artifacts from the completed workflow run:

| Artifact | Contents |
| --- | --- |
| `hostlier-controller-linux-amd64` | Linux AMD64 installation archive, including `collector-image/`, and SHA-256 checksum |
| `hostlier-controller-linux-arm64` | Linux ARM64 installation archive, including `collector-image/`, and SHA-256 checksum |
| `hostlier-cli-linux-amd64` / `hostlier-cli-linux-arm64` | Standalone Linux CLI archive and checksum |

The two executables have separate entry points and dependency graphs:
`hostlier` connects, deploys and manages local packages.
`hostlier-controller` installs the server, runs the daemon and handles offline recovery. The CLI
does not link controller runtime or state-store code; the controller does not
link the CLI or local dashboard. CLI aliases for daemon and recovery
commands start the separate sibling executable.
CI checks this separation before building. Controller archives contain
`hostlier-controller` and `collector-image/`; CLI archives contain `hostlier` and
`minion/`, which holds the framework package for Hostlier Minion. Keep `minion/`
beside the binary when installing the archive; a symlink to the binary is supported.
Each CLI is smoke-tested on its native architecture. The controller job checks
both archives and runs the Linux AMD64 executable.
Workflow artifacts are retained for 14 days. These workflows do not deploy servers.
A version tag matching `v*`
also runs the controller workflow's tests and publishes its two checksum-verified
Linux controller bundles as a GitHub Release. The publish job accepts only the
official `Hostlier/hostlier` repository, an existing pushed tag, and a
release commit reachable from `main`. The release assets provide the durable
source for `hostlier-controller update`.

After downloading and unzipping a GitHub artifact, verify the adjacent checksum
before extracting its tar archive:

```sh
shasum -a 256 -c hostlier-cli-linux-amd64.tar.gz.sha256
tar -xzf hostlier-cli-linux-amd64.tar.gz
./hostlier help
```

Use the controller archive for server installation, preserving `collector-image/`
beside `hostlier-controller`, then run `sudo ./hostlier-controller init`. The
controller installer does not require or install the CLI. Linux CLI archives are
available from CI; build the CLI from source for macOS. Minion development also
requires Node.js 24 or newer. Run
`hostlier minion install` in an app directory to install the bundled framework, or
run `hostlier minion dev` to install it automatically and start the local app.
Hostlier preserves its package under `.minion/framework/` so npm can reproduce the
installation from the saved package and lockfile. Keep this framework archive with
your app source; it contains framework code, not credentials. For a matched pair, download artifacts built from the same commit; use manual
workflow runs when one component did not change.

To produce a standalone CLI archive locally, run `make cli-release`, or explicitly
select a supported target with `./scripts/package-cli.sh linux arm64`.

`--repository GIT_URL` supports provider-independent package discovery and installation through a versioned `hostlier-packages.json` manifest. See [Git package catalogs](../git-package-catalog.md) for the schema, authentication, and immutable version selection.
