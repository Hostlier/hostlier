#!/bin/sh
# Build both platforms, then atomically publish controller-served release assets.
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
./build.sh arm64
./build.sh amd64
trust_image='otel/opentelemetry-collector-contrib:0.164.0@sha256:340c9ca6a36617084c6661bdc4f0fe92b7cda5a805d61673ec8db375fddc522b'
expected_ca='5961c0a3c65459931c023e0075c09733f09aea4ca439cb85bc4a08bb4f3f568b'
ca_digest() {
  if [ ! -f dist/ca-certificates.crt ]; then return; fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum dist/ca-certificates.crt | awk '{print $1}'
  else
    shasum -a 256 dist/ca-certificates.crt | awk '{print $1}'
  fi
}
# Reuse only a byte-identical cached trust bundle from the pinned distribution.
if [ "$(ca_digest)" != "$expected_ca" ]; then
  trust_container=$(docker create "$trust_image")
  trap 'docker rm "$trust_container" >/dev/null' EXIT HUP INT TERM
  docker cp "$trust_container:/etc/ssl/certs/ca-certificates.crt" dist/ca-certificates.crt
  docker rm "$trust_container" >/dev/null
  trap - EXIT HUP INT TERM
fi
if [ "$(ca_digest)" != "$expected_ca" ]; then
  printf '%s\n' 'Pinned collector certificate bundle checksum mismatch' >&2
  exit 1
fi
cd ..
unset GOOS GOARCH GOARM GOARM64 GOAMD64 CGO_ENABLED GOFLAGS
export GOWORK=off
staging=$(mktemp -d collector/dist/.collector-image.XXXXXX)
cleanup() {
  # Restore the previous valid release if publication failed after moving it aside.
  if [ -d "$staging/previous" ] && [ ! -e collector/dist/collector-image ]; then
    mv "$staging/previous" collector/dist/collector-image
  fi
  rm -rf "$staging"
}
trap cleanup EXIT HUP INT TERM
GOTOOLCHAIN=go1.27.3 go run ./collector/package-image.go \
  --ca collector/dist/ca-certificates.crt --output "$staging/image"
if [ -L collector/dist/collector-image ]; then
  printf '%s\n' 'Refusing to replace a symlinked release image directory' >&2
  exit 1
fi
if [ -e collector/dist/collector-image ]; then
  mv collector/dist/collector-image "$staging/previous"
fi
mv "$staging/image" collector/dist/collector-image
