#!/bin/sh
# Package the standalone client executable for a laptop or Linux CLI without server installation assets.
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
# Use the reviewed security release for every packaged client and bundled minion.
export GOTOOLCHAIN=go1.27.3
target_os=${1:-$(go env GOOS)}
target_arch=${2:-$(go env GOARCH)}
# The client supports these Unix targets.
case "$target_os/$target_arch" in
  linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;;
  *) printf '%s\n' 'Usage: package-cli.sh [linux|darwin] [amd64|arm64]' >&2; exit 2 ;;
esac
if [ "$#" -gt 2 ]; then
  printf '%s\n' 'Usage: package-cli.sh [linux|darwin] [amd64|arm64]' >&2
  exit 2
fi
mkdir -p dist/releases
archive="hostlier-cli-$target_os-$target_arch.tar.gz"
staging=$(mktemp -d dist/releases/.hostlier-cli-XXXXXX)
trap 'rm -rf "$staging"' EXIT HUP INT TERM
cli_ldflags='-s -w'
# Packaged clients can locate same-commit Linux bundles without copying credentials to a box.
if [ "${GITHUB_ACTIONS:-}" = true ] && [ -n "${GITHUB_REPOSITORY:-}" ] && [ -n "${GITHUB_SHA:-}" ]; then
  cli_ldflags="$cli_ldflags -X hostlier/internal/cli.serverArtifactRepository=$GITHUB_REPOSITORY -X hostlier/internal/cli.serverArtifactCommit=$GITHUB_SHA"
fi
CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOWORK=off \
  go build -mod=readonly -trimpath -ldflags="$cli_ldflags" -o "$staging/hostlier" ./cmd/hostlier
mkdir -p "$staging/packages"
CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOWORK=off \
  go build -mod=readonly -trimpath -ldflags='-s -w' -o "$staging/packages/minion" ./packages/minion/cmd/hostlier-minion
./scripts/package-minion.sh "$staging/minion"
COPYFILE_DISABLE=1 tar -czf "$staging/$archive" -C "$staging" hostlier minion packages
mv "$staging/$archive" "dist/releases/$archive"
# A relative archive name makes verification work after downloading or copying the pair.
if command -v sha256sum >/dev/null 2>&1; then
  (cd dist/releases && sha256sum "$archive" > "$archive.sha256")
else
  (cd dist/releases && shasum -a 256 "$archive" > "$archive.sha256")
fi
printf 'Created dist/releases/%s\n' "$archive"
