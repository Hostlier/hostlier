# Engine-owned Fused runtime

This directory holds a compiled distribution of `@fused/runtime`, not its source.
The source and tests live in `Usefused/engine` under `runtime/sdk`. Minion installs
this archive through its lockfile and bundles it in its own release package.
Hostlier builds do not need an Engine checkout or an unpublished npm package.

To update, run `npm ci && npm test && npm run pack` in Engine's `runtime/sdk`.
Replace the `.tgz` and `manifest.json` here with `dist/package/` output, then update
Minion's dependency path for a new version and regenerate its package lock:

```sh
npm install --package-lock-only --ignore-scripts --prefix packages/minion
npm ci --prefix packages/minion
npm --prefix packages/minion test
npm --prefix packages/minion run pack
```

The manifest records the archive SHA-256 and Engine source-input digest. npm also
checks the archive against the lockfile integrity. Never edit the compiled archive
or implement runtime changes here. The same package is available through
`fused-cli app runtime-client` for standalone applications.
