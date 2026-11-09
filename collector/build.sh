#!/bin/sh
# Cross-compile a static, stripped collector using pinned source, modules and Go.
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
architecture=${1:-arm64}
case "$architecture" in
  arm64|amd64) ;;
  *) printf '%s\n' 'Usage: ./build.sh [arm64|amd64]' >&2; exit 2 ;;
esac
unset GOOS GOARCH GOARM GOARM64 GOAMD64 CGO_ENABLED GOFLAGS
export GOTOOLCHAIN=go1.27.3 GOWORK=off
./generate.sh
mkdir -p dist
cd dist/generated
go mod download
go mod verify
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build \
  -mod=readonly -trimpath -buildvcs=false -tags=grpcnotrace \
  -ldflags='-s -w -buildid=' \
  -o "../hostlier-collector-linux-$architecture" .
cd ../..
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "dist/hostlier-collector-linux-$architecture"
else
  shasum -a 256 "dist/hostlier-collector-linux-$architecture"
fi
wc -c "dist/hostlier-collector-linux-$architecture"
