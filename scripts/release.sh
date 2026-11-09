#!/bin/sh
# Produce self-contained releases: separate CLI and controller executables plus minimal collector OCI assets.
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
export GOTOOLCHAIN=go1.27.3
./collector/package-image.sh
mkdir -p dist/releases
for architecture in arm64 amd64; do
  release="dist/releases/hostlier-controller-linux-$architecture"
  # A fresh directory prevents accidentally packaging files from an older release.
  staging=$(mktemp -d "dist/releases/.hostlier-$architecture-XXXXXX")
  trap 'rm -rf "$staging"' EXIT HUP INT TERM
  CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -mod=readonly -trimpath -ldflags='-s -w' -o "$staging/hostlier-controller" ./cmd/hostlier-controller
  cp -R collector/dist/collector-image "$staging/collector-image"
  # The archive's two top-level entries work with automatic sibling-image discovery.
  COPYFILE_DISABLE=1 tar -czf "$release.tar.gz.tmp" -C "$staging" hostlier-controller collector-image
  mv "$release.tar.gz.tmp" "$release.tar.gz"
  rm -rf "$staging"
  trap - EXIT HUP INT TERM
  if command -v sha256sum >/dev/null 2>&1; then
    (cd dist/releases && sha256sum "hostlier-controller-linux-$architecture.tar.gz" > "hostlier-controller-linux-$architecture.tar.gz.sha256")
  else
    (cd dist/releases && shasum -a 256 "hostlier-controller-linux-$architecture.tar.gz" > "hostlier-controller-linux-$architecture.tar.gz.sha256")
  fi
  printf 'Created %s.tar.gz\n' "$release"
done
