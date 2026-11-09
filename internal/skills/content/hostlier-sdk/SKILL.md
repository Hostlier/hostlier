---
name: hostlier-sdk
description: Install the standalone Hostlier package-authoring Go SDK into a local development project.
---

# Hostlier SDK

For a self-hosted app vendor building customer cloud instances from a bundled `.hostlier`, install the separate Node client carried by the Hostlier CLI:

```sh
hostlier sdk install cloud --directory ./backend
hostlier sdk install cloud-go --directory ./go-backend
```

Both clients are bundled with the CLI and use its deployment, scale, and status commands. The Node install needs no npm or registry access; the Go install adds the YAML parser requirement to the existing `go.mod` and needs a Go toolchain. See `packages/cloud-sdk/README.md` and `packages/cloud-sdk-go/README.md`. The versioned Go installation below is for authoring Hostlier executable packages.

Use `SetInstanceSecret(ctx, app, name, value)` in Go or `setInstanceSecret(app, name, value)` in Node for private instance credentials. The controller binds these secrets to that app and removes them after its successful `delete --purge`; ordinary `SetSecret`/`setSecret` creates controller-wide secrets. Use `Purge` or `purge` for expiry cleanup and recover interrupted results with `RecoverPendingPurge` or `recoverPendingPurge`.

Install the package-authoring SDK into an existing local Go module:

```sh
go mod init example.com/my-hostlier-package
hostlier sdk install --version v0.1.0
# Or select an existing project:
hostlier sdk install --version COMMIT_OR_TAG --directory ./my-package
```

The requested ref must exist in `Usefused/hostlier-packages`. The CLI resolves it to an immutable commit, then runs `go get` for `github.com/Usefused/hostlier-packages/sdk/packagekit` at that commit. Go and Git must be installed locally. `go.mod` and `go.sum` record the dependency; no SDK executable or server process is installed.

This SDK provides the Go package-authoring helpers and wire contracts in the packages repository. It is distinct from the broader hosting-sdk project. Import `github.com/Usefused/hostlier-packages/sdk/packagekit` when writing packages; workflow and resource contracts are adjacent SDK packages.

While the repository is private, authenticate locally with `gh auth login`, `GH_TOKEN`, or `GITHUB_TOKEN`. The CLI scopes private-module and Git authentication settings to the installation subprocess. It does not save a token in the project or modify global Git/Go settings. Later direct Go commands still need your usual private-module authentication.

SDK installation is local development work. Use `hostlier package install NAME --version RELEASE_TAG --server NAME` to install a runnable package on a server. Source definitions and example workflows remain browsable in the packages repository for users with access.
