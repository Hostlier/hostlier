#!/bin/sh
# Fail CI if either executable starts embedding the other executable's implementation.
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cli_dependencies=$(go list -mod=readonly -deps ./cmd/hostlier)
controller_dependencies=$(go list -mod=readonly -deps ./cmd/hostlier-controller)
if printf '%s\n' "$cli_dependencies" | grep -E '^hostlier/internal/(installer|controller|controllercmd|runtime|store|recovery|observation)(/|$)'; then
  printf '%s\n' 'CLI must not embed server implementation packages' >&2
  exit 1
fi
if printf '%s\n' "$controller_dependencies" | grep -E '^hostlier/internal/(cli|client|localui|skills)(/|$)'; then
  printf '%s\n' 'Controller must not embed the client command runner' >&2
  exit 1
fi
printf '%s\n' 'Controller and CLI dependency boundaries verified'
