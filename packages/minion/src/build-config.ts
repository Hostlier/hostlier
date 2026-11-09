import { stat } from 'node:fs/promises';
import { resolve } from 'node:path';
import type { BuildOptions } from './build.js';

/** Resolve authored paths relative to the app, independent of the invoking command's cwd. */
export async function resolveBuildConfig(root: string, config: Partial<BuildOptions>): Promise<BuildOptions> {
  let entry = config.entry;
  if (entry === undefined) {
    for (const candidate of ['app.ts', 'src/app.ts', 'app.tsx', 'src/app.tsx']) {
      if (await stat(resolve(root, candidate)).then(info => info.isFile(), () => false)) {
        entry = candidate;
        break;
      }
    }
  }
  if (typeof entry !== 'string' || !entry) throw new Error('Set entry in minion.json to the server entry file');
  return {
    ...config,
    name: config.name ?? 'minion',
    entry: resolve(root, entry),
    outdir: resolve(root, config.outdir || 'build'),
    typesDirectory: resolve(root, config.typesDirectory ?? '.minion'),
    ui: config.ui && {
      ...config.ui,
      entry: resolve(root, config.ui.entry),
      load: config.ui.load && resolve(root, config.ui.load),
    },
  };
}
