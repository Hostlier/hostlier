#!/bin/sh
# Generate factory registration from the pinned OCB manifest; retain locked modules.
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
unset GOOS GOARCH GOARM GOARM64 GOAMD64 CGO_ENABLED GOFLAGS
export GOTOOLCHAIN=go1.27.3 GOWORK=off
mkdir -p dist
# Generation runs on the laptop architecture. Cross-compilation happens in build.sh.
go run go.opentelemetry.io/collector/cmd/builder@v0.164.0 \
  --config builder-config.yaml --skip-get-modules --skip-compilation
cp lock/go.mod dist/generated/go.mod
cp lock/go.sum dist/generated/go.sum
# Fail when a manifest edit has not been accompanied by an explicit lock update.
(cd dist/generated && go list -m -f '{{.Path}} {{.Version}}' all) > dist/generated/versions.txt
awk 'FNR == NR { versions[$1] = $2; next }
  $1 == "-" && $2 == "gomod:" && versions[$3] != $4 {
    print "Manifest/lock version mismatch: " $3 " expected " $4 > "/dev/stderr"
    failed = 1
  }
  END { exit failed }' dist/generated/versions.txt builder-config.yaml
