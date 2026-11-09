# Dependency security scan — 2026-11-04

## Go release follow-up

The project subsequently moved its module minimum, release workflows, packaging scripts, and Collector builder image to Go 1.27.3, the latest stable release on this date. The earlier Go 1.26.10 verification run is historical; the current Go 1.27.3 package checks are detailed in [Go 1.27.3 and package infrastructure verification](go-1.27.3-package-infrastructure.md).

## Scope and method

- Scanned the root Go module, `packages/pulumi`, the generated Collector module, and `internal/runtime/testdata/endpoint-probe` with `govulncheck v1.9.0` using Go 1.27.3.
- Ran `npm audit --package-lock-only` for `ui`, `packages/minion`, `examples/ollama-chat`, and `examples/jamjam`.
- Checked pinned Go, Nginx, Caddy, and k3s versions against their upstream release or Ubuntu security notices. The repository has no connected Security Cloud GitHub repository, so this is a local dependency scan, not a hosted repository scan or a scan of a deployed server.
- Checked the existing Docker package pin against the Ubuntu 24.04 package index; `29.1.3-0ubuntu3~24.04.2` is already the current security package.

## Remediation

| Component | Updated version | Reason |
| --- | --- | --- |
| Root Go module | `github.com/jackc/pgx/v5` 5.9.2; `github.com/redis/go-redis/v9` 9.7.3 | Fix reachable `govulncheck` findings. |
| Pulumi Go module | `go-git` 5.19.2, `go-billy` 5.9.0, `glog` 1.2.5, `circl` 1.6.3, gRPC 1.83.2, `x/net` 0.62.0 and their required transitive updates | Fix reachable `govulncheck` findings. |
| Collector lock | `golang.org/x/net` 0.62.0 | Fix the `x/net/html` findings in the generated Collector module. |
| Go build toolchain | 1.27.3 in release workflows, Collector build, and CLI packaging | Use the latest stable Go security release for shipped binaries. |
| Hostlier owned Caddy | 2.11.8 | Install an upstream release archive with architecture specific SHA256 verification into `/usr/local/bin/hostlier-caddy` and use it in the owned systemd service. |
| Hostlier owned Nginx | Ubuntu 24.04 package 1.24.0-2ubuntu7.19 or newer | Upgrade and verify the security package before enabling the owned edge. Require this floor for an existing gateway package. |
| Hostlier owned k3s | v1.34.12+k3s1 | Verify the architecture specific release checksum, replace the previous owned patch release, and restart `k3s.service`. |

The Nginx coexistence mode retains the operator's package and service. The gateway mode rejects an existing package below the security floor for operator review.
The Ubuntu Caddy package remains installed for its service account and unit; Hostlier's owned unit executes the verified 2.11.8 binary. Package inventory tools will still see the older Ubuntu package.

## Verification

- Post update `govulncheck` reported **zero reachable findings** in all four Go modules. The root, Pulumi, and Collector modules each retain only module level `GO-2026-5932` for `golang.org/x/crypto/openpgp`; no imported package or call trace reaches it.
- All four npm audits reported zero vulnerabilities.
- The Pulumi and generated Collector module suites passed under Go 1.27.3. Collector source regeneration passed. Hostlier root tests passed in focused runs, but the full root suite has no clean end-to-end result in this workspace because of concurrent source edits and, on a final rerun, disk and localhost sandbox limits; see the follow-up verification report.
- Binary and service changes were tested in the repository. No live Ubuntu server was upgraded or tested by this scan.

## Primary sources

- Go releases: https://go.dev/doc/devel/release
- Caddy v2.11.8: https://github.com/caddyserver/caddy/releases/tag/v2.11.8
- Ubuntu Nginx security notice USN-8955-1: https://ubuntu.com/security/notices/USN-8955-1
- k3s v1.34.12+k3s1: https://github.com/k3s-io/k3s/releases/tag/v1.34.12%2Bk3s1
- Ubuntu Docker package: https://packages.ubuntu.com/noble/docker.io
