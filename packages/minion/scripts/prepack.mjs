// npm run pack reports npm_command=run (run-script is accepted for the explicit alias).
// The stage step normalizes the vendored runtime dependency before publishing a portable archive.
if (!['run', 'run-script'].includes(process.env.npm_command ?? '')) {
  throw new Error('Use npm run pack to stage a canonical Minion archive; direct npm pack/publish is unsafe for this source checkout');
}
// The pack script compiles Minion and bundles Engine's installed runtime; this guard does not rebuild.
