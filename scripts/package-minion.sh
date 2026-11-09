#!/bin/sh
# Build the framework payload shipped with Hostlier so first-run installation needs no separate release lookup.
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
if [ "$#" -ne 1 ]; then
  printf '%s\n' 'Usage: package-minion.sh OUTPUT_DIRECTORY' >&2
  exit 2
fi
destination=$1
# npm --prefix changes the script cwd, so freeze the output path before invoking it.
case "$destination" in
  /*) ;;
  *) destination="$(pwd)/$destination" ;;
esac
# The lockfile verifies Engine's vendored runtime archive; no Engine source checkout is required.
npm ci --prefix packages/minion --ignore-scripts --no-audit --no-fund
npm --prefix packages/minion run pack -- --pack-destination "$destination"
